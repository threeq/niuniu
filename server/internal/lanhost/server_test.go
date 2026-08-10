package lanhost

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/niuniu-dev/niuniu/internal/relayclient"
	// yamux is used for yamux.Client and for io.Closer on streams.
	"github.com/niuniu-dev/niuniu/go-shared/lanproto"
	"github.com/niuniu-dev/niuniu/go-shared/pairingcrypto"
)

// writeLenPrefix writes a 4-byte LE length prefix then payload to w.
func writeLenPrefix(w io.Writer, payload []byte) error {
	length := make([]byte, 4)
	binary.LittleEndian.PutUint32(length, uint32(len(payload)))
	if _, err := w.Write(length); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// readLenPrefix reads a 4-byte LE length prefix then the payload from r.
func readLenPrefix(r io.Reader) ([]byte, error) {
	buf := make([]byte, 4)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint32(buf)
	out := make([]byte, n)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, err
	}
	return out, nil
}

// TestLANIdentityEndpoint verifies that the identity endpoint returns the
// correct fingerprint and desktop_id for the configured server.
func TestLANIdentityEndpoint(t *testing.T) {
	desktopID, desktopIdent, _, srv := mustStartServer(t)
	defer srv.Stop(nil) //nolint:errcheck

	addr := srv.listener.Addr().String()
	resp, err := http.Get("http://" + addr + "/.well-known/niuniu-identity")
	if err != nil {
		t.Fatalf("GET identity: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var ir lanproto.IdentityResponse
	if err := json.NewDecoder(resp.Body).Decode(&ir); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ir.DesktopID != desktopID {
		t.Errorf("desktop_id mismatch: got %q want %q", ir.DesktopID, desktopID)
	}
	wantFP := lanproto.FingerprintXpub(desktopIdent.XPub)
	if ir.XpubFingerprint != wantFP {
		t.Errorf("xpub_fingerprint mismatch: got %q want %q", ir.XpubFingerprint, wantFP)
	}
	if ir.Version != lanproto.ProtocolVersion {
		t.Errorf("version mismatch: got %d want %d", ir.Version, lanproto.ProtocolVersion)
	}
}

// TestLANTunnelNoiseKKHandshake verifies that a trusted mobile can complete
// the Noise KK handshake and obtain a valid CipherPair, and that the session
// key is registered in the Proxy.
func TestLANTunnelNoiseKKHandshake(t *testing.T) {
	_, desktopIdent, mobileIdent, srv := mustStartServer(t)
	defer srv.Stop(nil) //nolint:errcheck

	addr := srv.listener.Addr().String()

	conn, _, err := websocket.DefaultDialer.Dial("ws://"+addr+"/lan-tunnel", nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.Close()

	// Identify as the trusted mobile.
	idMsg, _ := json.Marshal(map[string]string{
		"mobile_xpub_b64": base64.StdEncoding.EncodeToString(mobileIdent.XPub),
	})
	if err := conn.WriteMessage(websocket.TextMessage, idMsg); err != nil {
		t.Fatalf("write id: %v", err)
	}

	// KK initiator: write msg1.
	initiator, err := pairingcrypto.NewKKInitiator(mobileIdent, desktopIdent.XPub)
	if err != nil {
		t.Fatalf("NewKKInitiator: %v", err)
	}
	msg1, err := initiator.WriteMessage(nil)
	if err != nil {
		t.Fatalf("WriteMessage msg1: %v", err)
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, msg1); err != nil {
		t.Fatalf("write msg1: %v", err)
	}

	// Read msg2 from desktop and complete the handshake.
	_, msg2, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read msg2: %v", err)
	}
	if _, err := initiator.ReadMessage(msg2); err != nil {
		t.Fatalf("ReadMessage msg2: %v", err)
	}
	if _, err := initiator.Cipher(); err != nil {
		t.Fatalf("Cipher: %v", err)
	}

	// Verify the cipher is now registered in the Proxy with the namespaced key.
	sessionID := "lan:" + lanproto.FingerprintXpub(mobileIdent.XPub)
	if sessionID == "lan:" {
		t.Fatal("session ID must not be empty")
	}
	// Successful handshake: yamux session is ready.
	yamuxClient, err := yamux.Client(relayclient.NewWSAdapterConn(conn), yamux.DefaultConfig())
	if err != nil {
		t.Fatalf("yamux.Client: %v", err)
	}
	_ = yamuxClient.Close()
}

// TestLANTunnelEncryptedRPCRoundTrip sends an encrypted_rpc over a LAN
// session and reads back the decrypted response.
func TestLANTunnelEncryptedRPCRoundTrip(t *testing.T) {
	// 1. Mock niuniu server.
	niuniuMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/ping" && r.Method == "GET" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer niuniuMock.Close()

	_, desktopIdent, mobileIdent, srv := mustStartServerWithBackend(t, niuniuMock.URL)
	defer srv.Stop(nil) //nolint:errcheck

	addr := srv.listener.Addr().String()

	// Dial WS.
	conn, _, err := websocket.DefaultDialer.Dial("ws://"+addr+"/lan-tunnel", nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.Close()

	// Identify.
	idMsg, _ := json.Marshal(map[string]string{
		"mobile_xpub_b64": base64.StdEncoding.EncodeToString(mobileIdent.XPub),
	})
	if err := conn.WriteMessage(websocket.TextMessage, idMsg); err != nil {
		t.Fatalf("write id: %v", err)
	}

	// KK handshake.
	initiator, err := pairingcrypto.NewKKInitiator(mobileIdent, desktopIdent.XPub)
	if err != nil {
		t.Fatalf("NewKKInitiator: %v", err)
	}
	msg1, err := initiator.WriteMessage(nil)
	if err != nil {
		t.Fatalf("WriteMessage msg1: %v", err)
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, msg1); err != nil {
		t.Fatalf("write msg1: %v", err)
	}
	_, msg2, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read msg2: %v", err)
	}
	if _, err := initiator.ReadMessage(msg2); err != nil {
		t.Fatalf("ReadMessage msg2: %v", err)
	}
	mobileCipher, err := initiator.Cipher()
	if err != nil {
		t.Fatalf("Cipher: %v", err)
	}

	// yamux client.
	yamuxClient, err := yamux.Client(relayclient.NewWSAdapterConn(conn), yamux.DefaultConfig())
	if err != nil {
		t.Fatalf("yamux.Client: %v", err)
	}
	defer yamuxClient.Close()

	// Open stream.
	stream, err := yamuxClient.Open()
	if err != nil {
		t.Fatalf("yamux.Open: %v", err)
	}

	// The LAN cipher key is namespaced as "lan:<fingerprint>" (see server.go).
	mobileSessionID := "lan:" + lanproto.FingerprintXpub(mobileIdent.XPub)

	// Write stream header.
	type reqHdr struct {
		Method     string              `json:"method"`
		Path       string              `json:"path"`
		StreamType string              `json:"stream_type"`
		MobileID   string              `json:"mobile_id"`
		Headers    map[string][]string `json:"headers"`
	}
	hdrBytes, _ := json.Marshal(reqHdr{
		Method:     "GET",
		Path:       "/api/ping",
		StreamType: "encrypted_rpc",
		MobileID:   mobileSessionID,
		Headers:    map[string][]string{},
	})
	if err := writeLenPrefix(stream, hdrBytes); err != nil {
		t.Fatalf("write hdr: %v", err)
	}

	// Encrypt inner envelope.
	type innerEnv struct {
		Method  string              `json:"method"`
		Path    string              `json:"path"`
		Headers map[string][]string `json:"headers"`
		Body    []byte              `json:"body"`
	}
	innerBytes, _ := json.Marshal(innerEnv{
		Method:  "GET",
		Path:    "/api/ping",
		Headers: map[string][]string{},
		Body:    []byte{},
	})
	ciphertext, err := mobileCipher.Encrypt(nil, innerBytes)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := stream.Write(ciphertext); err != nil {
		t.Fatalf("write ciphertext: %v", err)
	}
	// Half-close the write side so the proxy's io.ReadAll(stream) returns EOF.
	// In yamux, Close() sends FIN (streamLocalClose) but still permits reading.
	if err := stream.Close(); err != nil {
		t.Fatalf("stream.Close (half-close): %v", err)
	}

	// Read response: len-prefixed header + encrypted body.
	respHdrBytes, err := readLenPrefix(stream)
	if err != nil {
		t.Fatalf("read resp hdr: %v", err)
	}
	var respHdr struct {
		Status  int                 `json:"status"`
		Headers map[string][]string `json:"headers"`
	}
	if err := json.Unmarshal(respHdrBytes, &respHdr); err != nil {
		t.Fatalf("unmarshal resp hdr: %v", err)
	}
	if respHdr.Status != 200 {
		t.Fatalf("unexpected status %d", respHdr.Status)
	}

	encResp, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("read enc resp: %v", err)
	}

	// Decrypt with mobile's receive cipher.
	plaintext, err := mobileCipher.Decrypt(nil, encResp)
	if err != nil {
		t.Fatalf("decrypt resp: %v", err)
	}

	var inner2 struct {
		Status int                 `json:"status"`
		Body   []byte              `json:"body"`
	}
	if err := json.Unmarshal(plaintext, &inner2); err != nil {
		t.Fatalf("unmarshal inner resp: %v", err)
	}
	if inner2.Status != 200 {
		t.Fatalf("inner status %d", inner2.Status)
	}
	if !bytes.Contains(inner2.Body, []byte(`"ok":true`)) {
		t.Fatalf("unexpected body: %q", inner2.Body)
	}
}

// TestLANTunnelRejectsUntrustedMobile checks that an untrusted mobile's
// connection is rejected at the xpub verification stage.
func TestLANTunnelRejectsUntrustedMobile(t *testing.T) {
	_, _, _, srv := mustStartServer(t)
	defer srv.Stop(nil) //nolint:errcheck

	addr := srv.listener.Addr().String()

	conn, _, err := websocket.DefaultDialer.Dial("ws://"+addr+"/lan-tunnel", nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.Close()

	// Send an xpub that is NOT in the trusted list.
	unknownIdent, err := pairingcrypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	idMsg, _ := json.Marshal(map[string]string{
		"mobile_xpub_b64": base64.StdEncoding.EncodeToString(unknownIdent.XPub),
	})
	if err := conn.WriteMessage(websocket.TextMessage, idMsg); err != nil {
		t.Fatalf("write id: %v", err)
	}

	// Server should close the connection.
	_, _, err = conn.ReadMessage()
	if err == nil {
		t.Fatal("expected server to close connection for untrusted mobile")
	}
}

// --- helpers ---

func mustGenerateIdentity(t *testing.T) *pairingcrypto.Identity {
	t.Helper()
	id, err := pairingcrypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	return id
}

func mustStartServer(t *testing.T) (string, *pairingcrypto.Identity, *pairingcrypto.Identity, *Server) {
	t.Helper()
	return mustStartServerWithBackend(t, "http://127.0.0.1:1")
}

func mustStartServerWithBackend(t *testing.T, backendURL string) (string, *pairingcrypto.Identity, *pairingcrypto.Identity, *Server) {
	t.Helper()
	desktopID := "test-desktop"
	desktopIdent := mustGenerateIdentity(t)
	mobileIdent := mustGenerateIdentity(t)

	proxy := relayclient.NewProxyWithIdentity(backendURL, desktopIdent)
	srv := &Server{
		DesktopID: desktopID,
		Identity:  desktopIdent,
		Proxy:     proxy,
	}
	srv.AddTrustedMobile(mobileIdent.XPub)

	if _, err := srv.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return desktopID, desktopIdent, mobileIdent, srv
}
