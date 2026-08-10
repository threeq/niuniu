package relayclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/niuniu-dev/niuniu/go-shared/pairingcrypto"
	"github.com/niuniu-dev/niuniu/go-shared/relayproto"
	"github.com/niuniu-dev/niuniu/go-shared/tokenbind"
	"github.com/niuniu-dev/niuniu/go-shared/version"
)

// Tunnel is the desktop's side of the relay tunnel.
// After Dial, Serve accepts yamux streams and hands them to a Proxy.
type Tunnel struct {
	cfg     *Config
	session *yamux.Session
	id      *pairingcrypto.Identity
}

// NewTunnel builds a Tunnel that uses cfg and id (Ed25519 identity) for
// proof-of-possession during /tunnel handshake. Call Dial to open the WSS connection.
func NewTunnel(cfg *Config, id *pairingcrypto.Identity) *Tunnel {
	return &Tunnel{cfg: cfg, id: id}
}

// Dial opens a WSS to <relay>/tunnel with the desktop token, sends HELLO, and
// wraps the connection in a yamux client session.
func (t *Tunnel) Dial() error {
	u, err := url.Parse(t.cfg.RelayURL)
	if err != nil {
		return err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}
	u.Path = "/tunnel"

	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+t.cfg.DesktopToken)

	ws, _, err := websocket.DefaultDialer.Dial(u.String(), hdr)
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}
	// Build the full supported version range [MinSupported, ..., Protocol].
	supported := make([]uint16, 0, version.Protocol-version.MinSupported+1)
	for v := version.MinSupported; v <= version.Protocol; v++ {
		supported = append(supported, v)
	}
	hello := relayproto.Hello{
		DesktopID:         t.cfg.DesktopID,
		Version:           version.Protocol,
		SupportedVersions: supported,
	}
	buf, _ := json.Marshal(hello)
	if err := ws.WriteMessage(websocket.BinaryMessage, buf); err != nil {
		ws.Close()
		return err
	}

	// Read HELLO_ACK from relay (version negotiation).
	_, ackBuf, err := ws.ReadMessage()
	if err != nil {
		ws.Close()
		return fmt.Errorf("read hello_ack: %w", err)
	}
	var ack relayproto.HelloAck
	if err := json.Unmarshal(ackBuf, &ack); err != nil {
		ws.Close()
		return fmt.Errorf("unmarshal hello_ack: %w", err)
	}

	// Downgrade-attack defense: if we have a locally persisted negotiated version
	// that is HIGHER than what the relay just offered, abort.
	lastProto, _ := LoadLastProtocolVersion()
	if lastProto > 0 && ack.NegotiatedVersion < lastProto {
		ws.Close()
		return fmt.Errorf(
			"possible downgrade attack: relay offered version %d but we previously negotiated %d",
			ack.NegotiatedVersion, lastProto,
		)
	}
	// Persist max(stored, negotiated) — best-effort, never blocks tunnel open.
	_ = SaveLastProtocolVersion(ack.NegotiatedVersion)

	// Read CHALLENGE from relay.
	_, chBuf, err := ws.ReadMessage()
	if err != nil {
		ws.Close()
		return err
	}
	var ch struct {
		Nonce string `json:"nonce"`
		TS    int64  `json:"ts"`
	}
	if err := json.Unmarshal(chBuf, &ch); err != nil {
		ws.Close()
		return err
	}
	nonceBytes, err := base64.StdEncoding.DecodeString(ch.Nonce)
	if err != nil {
		ws.Close()
		return err
	}
	sig := tokenbind.SignTunnelChallenge(t.id, t.cfg.DesktopID, nonceBytes, ch.TS)
	proof := map[string]string{"sig": base64.StdEncoding.EncodeToString(sig)}
	proofBytes, _ := json.Marshal(proof)
	if err := ws.WriteMessage(websocket.BinaryMessage, proofBytes); err != nil {
		ws.Close()
		return err
	}

	sess, err := yamux.Client(newWSAdapter(ws), yamux.DefaultConfig())
	if err != nil {
		ws.Close()
		return err
	}
	t.session = sess
	return nil
}

// Session returns the active yamux session (nil until Dial succeeds).
func (t *Tunnel) Session() *yamux.Session { return t.session }

// Close closes the underlying yamux session.
func (t *Tunnel) Close() {
	if t.session != nil {
		_ = t.session.Close()
	}
}

// Serve accepts yamux streams and hands each to proxy.HandleStream on its own goroutine.
// Returns when the session is closed.
func (t *Tunnel) Serve(proxy *Proxy) error {
	for {
		stream, err := t.session.AcceptStream()
		if err != nil {
			return err
		}
		go proxy.HandleStream(stream)
	}
}

// RunForever dials and reconnects with exponential backoff up to 30 seconds.
// Each successful Dial results in a call to Serve; when Serve returns the
// session is closed and the loop sleeps before redialing.
func (t *Tunnel) RunForever(proxy *Proxy) {
	t.RunForeverCtx(context.Background(), proxy)
}

// RunForeverCtx is like RunForever but returns promptly once ctx is cancelled.
// Used by the desktop to tear down the relay connection on logout/credential
// change without leaking a goroutine.
func (t *Tunnel) RunForeverCtx(ctx context.Context, proxy *Proxy) {
	// Close the live yamux session as soon as ctx fires so Serve() returns.
	go func() {
		<-ctx.Done()
		t.Close()
	}()

	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		if err := t.Dial(); err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		_ = t.Serve(proxy)
		t.Close()
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// We want io as an explicit dep for readability of callers.
var _ io.Reader = (*wsAdapter)(nil)
