// Package lanhost implements the desktop's LAN fast-path listener.
// Paired mobiles on the same network connect directly here instead of
// traversing the relay.
package lanhost

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/niuniu-dev/niuniu/internal/relayclient"
	"github.com/niuniu-dev/niuniu/go-shared/lanproto"
	"github.com/niuniu-dev/niuniu/go-shared/pairingcrypto"
)

// Server runs the LAN listener for direct mobile connections.
type Server struct {
	// DesktopID is the stable identifier for this desktop (used in mDNS TXT and
	// the identity endpoint response).
	DesktopID string

	// Scope identifies which niuniu user's trusted-mobiles list this LAN
	// server is serving — propagated to relayclient.MarkSeen so last-seen
	// updates go to the right scope.  Empty → DefaultTrustedMobilesScope.
	Scope string

	// Identity is the desktop's long-term Noise KK identity.
	Identity *pairingcrypto.Identity

	// Proxy is the transport-agnostic stream dispatcher shared with the relay
	// tunnel.  Ciphers established over LAN are injected via Proxy.SetCipher.
	Proxy *relayclient.Proxy

	// TrustedMobileXpubs lists the X25519 public keys of mobiles this desktop
	// has paired with. A KK handshake can only succeed against a pinned xpub, so
	// the list gates which mobiles may connect.
	TrustedMobileXpubs [][]byte

	listener net.Listener
	httpSrv  *http.Server
	mu       sync.Mutex

	// pairMode is set to 1 while LAN-only pairing is active.
	// The /pair/claim route is always registered; it checks this flag and returns
	// 404 when inactive, avoiding a racy handler-swap on httpSrv.Handler.
	pairMode atomic.Int32

	// pairResultCh receives the first successful PairSession from handlePairClaim.
	// Set by StartLanOnlyPairing while active; cleared on return.
	pairResultCh chan *PairSession

	// pairIdentity is the desktop identity sent in /pair/claim responses.
	pairIdentity     *pairingcrypto.Identity
	pairDesktopID    string
	pairDesktopName  string
}

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

// Start binds to addr ("0.0.0.0:0" picks a free port), registers HTTP
// handlers, and starts serving in a background goroutine.
// Returns the bound address.
func (s *Server) Start(addr string) (net.Addr, error) {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s.listener = l

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/niuniu-identity", s.handleIdentity)
	mux.HandleFunc("/lan-tunnel", s.handleLanTunnel)
	// /pair/claim is always registered; it checks s.pairMode and returns 404
	// when pairing is not active.  This avoids a racy swap of httpSrv.Handler
	// at runtime (see StartLanOnlyPairing).
	mux.HandleFunc("/pair/claim", s.handlePairClaim)
	s.httpSrv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = s.httpSrv.Serve(l) }()
	return l.Addr(), nil
}

// Stop shuts down the HTTP server gracefully.
func (s *Server) Stop(ctx context.Context) error {
	if s.httpSrv != nil {
		return s.httpSrv.Shutdown(ctx)
	}
	return nil
}

// AddTrustedMobile adds a pinned mobile X25519 public key to the trusted set.
func (s *Server) AddTrustedMobile(xpub []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.TrustedMobileXpubs = append(s.TrustedMobileXpubs, append([]byte(nil), xpub...))
}

// RemoveTrustedMobile removes the mobile identified by xpubHex (hex-encoded) from the
// trusted set. Future connection attempts from that mobile will be rejected.
func (s *Server) RemoveTrustedMobile(xpubHex string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	filtered := s.TrustedMobileXpubs[:0]
	for _, xpub := range s.TrustedMobileXpubs {
		if encodeHex(xpub) != xpubHex {
			filtered = append(filtered, xpub)
		}
	}
	s.TrustedMobileXpubs = filtered
}

// handleIdentity serves GET /.well-known/niuniu-identity.
// The mobile fetches this to confirm it is talking to the right desktop before
// committing to the KK handshake.
func (s *Server) handleIdentity(w http.ResponseWriter, _ *http.Request) {
	resp := lanproto.IdentityResponse{
		Version:         lanproto.ProtocolVersion,
		DesktopID:       s.DesktopID,
		XpubFingerprint: lanproto.FingerprintXpub(s.Identity.XPub),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleLanTunnel upgrades the HTTP connection to WebSocket, performs a Noise
// KK responder handshake for the connecting mobile, then serves yamux streams
// via the shared Proxy.
//
// Protocol (desktop side):
//
//  1. Mobile sends JSON {"mobile_xpub_b64":"<base64 std>"}.
//  2. Desktop verifies xpub is in TrustedMobileXpubs; else closes.
//  3. Mobile sends Noise KK msg1 as a binary WebSocket frame.
//  4. Desktop runs NewKKResponder(self, mobileXpub), reads msg1, writes msg2.
//  5. Desktop wraps the WS conn in yamux.Server and loops serving streams.
func (s *Server) handleLanTunnel(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	// Step 1: mobile identifies itself.
	_, idBytes, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		return
	}
	var idMsg struct {
		MobileXpubB64 string `json:"mobile_xpub_b64"`
	}
	if err := json.Unmarshal(idBytes, &idMsg); err != nil {
		conn.Close()
		return
	}
	mobileXpub, err := base64.StdEncoding.DecodeString(idMsg.MobileXpubB64)
	if err != nil || !s.isTrusted(mobileXpub) {
		conn.Close()
		return
	}

	// Step 2: Noise KK responder handshake.
	responder, err := pairingcrypto.NewKKResponder(s.Identity, mobileXpub)
	if err != nil {
		conn.Close()
		return
	}
	_, msg1, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		return
	}
	if _, err := responder.ReadMessage(msg1); err != nil {
		conn.Close()
		return
	}
	msg2, err := responder.WriteMessage(nil)
	if err != nil {
		conn.Close()
		return
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, msg2); err != nil {
		conn.Close()
		return
	}
	cp, err := responder.Cipher()
	if err != nil {
		conn.Close()
		return
	}
	// §7.5: rekey every 100,000 frames (≈100 MB at 1 KB avg) to bound key exposure.
	cp.SetRekeyInterval(100000)

	// Register the cipher in the shared Proxy so encrypted_rpc streams can use it.
	// Namespace the key as "lan:<xpub_fingerprint>" to avoid collision with relay
	// sessions keyed as "relay:<mobile_id>" (see proxy.go handleHandshake).
	mobileSessionID := "lan:" + lanproto.FingerprintXpub(mobileXpub)
	s.Proxy.SetCipher(mobileSessionID, cp)

	// Update last-seen timestamp for this mobile (best-effort; ignore errors).
	_ = relayclient.MarkSeen(s.Scope, mobileSessionID)

	// Prevent system sleep while the LAN session is active.
	relayclient.NotifySessionOpen()
	defer relayclient.NotifySessionClose()

	// Step 3: wrap in yamux and serve streams.
	netConn := relayclient.NewWSAdapterConn(conn)
	ses, err := yamux.Server(netConn, yamux.DefaultConfig())
	if err != nil {
		conn.Close()
		return
	}
	defer ses.Close()
	for {
		stream, err := ses.AcceptStream()
		if err != nil {
			return
		}
		go s.Proxy.HandleStream(stream)
	}
}

// handlePairClaim handles POST /pair/claim.
// It is always registered on the mux; when pairMode is inactive it returns 404
// so the endpoint is effectively invisible until StartLanOnlyPairing activates it.
func (s *Server) handlePairClaim(w http.ResponseWriter, r *http.Request) {
	if s.pairMode.Load() == 0 {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.Lock()
	resultCh := s.pairResultCh
	identity := s.pairIdentity
	desktopID := s.pairDesktopID
	desktopName := s.pairDesktopName
	s.mu.Unlock()

	if resultCh == nil {
		http.NotFound(w, r)
		return
	}

	var req LanOnlyPairRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.MobileXpubB64 == "" || req.MobileSignPubB64 == "" {
		http.Error(w, "missing keys", http.StatusBadRequest)
		return
	}
	mobileXpub, err := base64.StdEncoding.DecodeString(req.MobileXpubB64)
	if err != nil || len(mobileXpub) == 0 {
		http.Error(w, "invalid mobile_xpub_b64", http.StatusBadRequest)
		return
	}
	mobileSignPub, err := base64.StdEncoding.DecodeString(req.MobileSignPubB64)
	if err != nil || len(mobileSignPub) == 0 {
		http.Error(w, "invalid mobile_sign_pub_b64", http.StatusBadRequest)
		return
	}

	resp := LanOnlyPairResponse{
		DesktopID:         desktopID,
		DesktopName:       desktopName,
		DesktopXpubB64:    base64.StdEncoding.EncodeToString(identity.XPub),
		DesktopSignPubB64: base64.StdEncoding.EncodeToString(identity.EdPub),
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		return
	}

	select {
	case resultCh <- &PairSession{
		MobileXpub:     mobileXpub,
		MobileSignPub:  mobileSignPub,
		MobileName:     req.MobileName,
		MobilePlatform: req.MobilePlatform,
	}:
	default:
		// Another claim already succeeded; ignore this one.
	}
}

func (s *Server) isTrusted(xpub []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.TrustedMobileXpubs {
		if bytesEqual(t, xpub) {
			return true
		}
	}
	return false
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func encodeHex(b []byte) string {
	return hex.EncodeToString(b)
}
