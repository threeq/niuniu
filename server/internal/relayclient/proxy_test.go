package relayclient

import (
	"bytes"
	stdbase64 "encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/niuniu-dev/niuniu/go-shared/pairingcrypto"
)

// pipeRWC is a io.ReadWriteCloser for tests, built on a pair of io.Pipe ends.
type pipeRWC struct {
	io.Reader
	io.Writer
	closers []io.Closer
	once    sync.Once
}

func (p *pipeRWC) Close() error {
	p.once.Do(func() {
		for _, c := range p.closers {
			_ = c.Close()
		}
	})
	return nil
}

// TestEncryptedRPC_BodyRoundTrip verifies that an encrypted RPC request whose
// inner envelope uses the standard "body" JSON field (base64-encoded []byte)
// arrives at the local mock server with the correct body bytes.
//
// This tests the Go side of Bug F2: the innerEnvelope struct uses
//   Body []byte `json:"body"`
// and encoding/json automatically decodes a base64 string into []byte.
// If the mobile side were to use "body_b64" instead the body would be empty.
func TestEncryptedRPC_BodyRoundTrip(t *testing.T) {
	const wantBody = `{"msg":"hello"}`
	var capturedBody []byte

	// Mock local niuniu server that echoes the request body back.
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer mock.Close()

	// Generate a Noise KK identity for the desktop (responder).
	desktopID, err := pairingcrypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	// Generate an identity for the mobile (initiator).
	mobileID, err := pairingcrypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity mobile: %v", err)
	}

	// Perform a Noise KK handshake to get a CipherPair on both sides.
	initiator, err := pairingcrypto.NewKKInitiator(mobileID, desktopID.XPub)
	if err != nil {
		t.Fatalf("NewKKInitiator: %v", err)
	}
	responder, err := pairingcrypto.NewKKResponder(desktopID, mobileID.XPub)
	if err != nil {
		t.Fatalf("NewKKResponder: %v", err)
	}
	msg1, err := initiator.WriteMessage(nil)
	if err != nil {
		t.Fatalf("initiator.WriteMessage: %v", err)
	}
	if _, err := responder.ReadMessage(msg1); err != nil {
		t.Fatalf("responder.ReadMessage: %v", err)
	}
	msg2, err := responder.WriteMessage(nil)
	if err != nil {
		t.Fatalf("responder.WriteMessage: %v", err)
	}
	if _, err := initiator.ReadMessage(msg2); err != nil {
		t.Fatalf("initiator.ReadMessage: %v", err)
	}
	mobileCipher, err := initiator.Cipher()
	if err != nil {
		t.Fatalf("initiator.Cipher: %v", err)
	}
	desktopCipher, err := responder.Cipher()
	if err != nil {
		t.Fatalf("responder.Cipher: %v", err)
	}

	proxy := NewProxyWithIdentity(mock.URL, desktopID)
	const mobileFingerprint = "test-mobile-id"
	proxy.SetCipher(mobileFingerprint, desktopCipher)

	// Build the inner envelope as the mobile would: body field = base64(bytes).
	inner := innerEnvelope{
		Method:  "POST",
		Path:    "/api/test",
		Headers: map[string][]string{"Content-Type": {"application/json"}},
		Body:    []byte(wantBody),
	}
	innerBytes, _ := json.Marshal(inner)
	ciphertext, err := mobileCipher.Encrypt(nil, innerBytes)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Build the stream: len-prefixed stream header + raw ciphertext (no len-prefix for
	// the body — handleEncryptedRPC reads it with io.ReadAll after the header).
	streamHdr, _ := json.Marshal(streamReqHeader{
		StreamType: "encrypted_rpc",
		MobileID:   mobileFingerprint,
	})

	relayToProxyR, relayToProxyW := io.Pipe()
	proxyToRelayR, proxyToRelayW := io.Pipe()
	proxyStream := &pipeRWC{Reader: relayToProxyR, Writer: proxyToRelayW, closers: []io.Closer{relayToProxyR, proxyToRelayW}}
	relaySide := &pipeRWC{Reader: proxyToRelayR, Writer: relayToProxyW, closers: []io.Closer{proxyToRelayR, relayToProxyW}}

	go func() {
		_ = writeLenPrefix(relaySide, streamHdr)
		_, _ = relaySide.Write(ciphertext)
		_ = relayToProxyW.Close()
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		proxy.HandleStream(proxyStream)
	}()

	// Read the response header to unblock the proxy.
	rhBytes, err := readLenPrefix(relaySide)
	if err != nil {
		t.Fatalf("readLenPrefix response: %v", err)
	}
	var rh streamRespHeader
	if err := json.Unmarshal(rhBytes, &rh); err != nil {
		t.Fatalf("unmarshal response header: %v", err)
	}
	if rh.Status != 200 {
		t.Fatalf("outer status %d", rh.Status)
	}
	// The remainder is the encrypted response blob — read it all.
	encResp, _ := io.ReadAll(relaySide)
	<-done

	// Decrypt the response to verify the round-trip worked.
	respPlain, err := mobileCipher.Decrypt(nil, encResp)
	if err != nil {
		t.Fatalf("Decrypt response: %v", err)
	}
	var innerResp innerResponse
	if err := json.Unmarshal(respPlain, &innerResp); err != nil {
		t.Fatalf("unmarshal inner response: %v", err)
	}
	if innerResp.Status != 200 {
		t.Fatalf("inner response status %d", innerResp.Status)
	}

	// Assert the request body arrived intact at the local server.
	if string(capturedBody) != wantBody {
		t.Fatalf("body mismatch: got %q, want %q", capturedBody, wantBody)
	}
}

func TestProxyHandleStream_HTTP(t *testing.T) {
	var capturedOrigin string
	var capturedMobileID string
	// local niuniu mock
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedOrigin = r.Header.Get("Origin")
		capturedMobileID = r.Header.Get("X-Niuniu-Mobile-ID")
		if r.URL.Path != "/api/ping" || r.Method != "GET" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer mock.Close()

	proxy := NewProxy(mock.URL)

	// Build a bidirectional pipe: relay writes request, proxy reads; proxy writes response, relay reads.
	relayToProxyR, relayToProxyW := io.Pipe()
	proxyToRelayR, proxyToRelayW := io.Pipe()
	proxyStream := &pipeRWC{Reader: relayToProxyR, Writer: proxyToRelayW, closers: []io.Closer{relayToProxyR, proxyToRelayW}}
	relaySide := &pipeRWC{Reader: proxyToRelayR, Writer: relayToProxyW, closers: []io.Closer{proxyToRelayR, relayToProxyW}}

	// relay pushes a request to the proxy — includes a MobileID to test X-Niuniu-Mobile-ID injection.
	go func() {
		hdr, _ := json.Marshal(streamReqHeader{Method: "GET", Path: "/api/ping", Headers: map[string][]string{}, MobileID: "mob-abc123"})
		_ = writeLenPrefix(relaySide, hdr)
		// no body; close the writer so proxy's io.ReadAll(stream) returns
		// Actually: the proxy only reads the header's length then io.ReadAll the body.
		// io.ReadAll reads until EOF — we close the write side of relayToProxy to signal EOF.
		_ = relayToProxyW.Close()
	}()

	// proxy runs in its own goroutine
	done := make(chan struct{})
	go func() {
		defer close(done)
		proxy.HandleStream(proxyStream)
	}()

	// relay reads the response
	rhBytes, err := readLenPrefix(relaySide)
	if err != nil {
		t.Fatalf("readLenPrefix: %v", err)
	}
	var rh streamRespHeader
	if err := json.Unmarshal(rhBytes, &rh); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rh.Status != 200 {
		t.Fatalf("status %d", rh.Status)
	}
	body, _ := io.ReadAll(relaySide)
	if !bytes.Equal(body, []byte(`{"ok":true}`)) {
		t.Fatalf("body mismatch: %q", body)
	}
	<-done

	// Assert Origin was rewritten to the local server's origin.
	// mock.URL is e.g. "http://127.0.0.1:12345" which equals the expected Origin.
	wantOrigin := mock.URL
	if capturedOrigin != wantOrigin {
		t.Fatalf("Origin header: got %q, want %q", capturedOrigin, wantOrigin)
	}
	// Assert X-Niuniu-Mobile-ID was injected.
	if capturedMobileID != "mob-abc123" {
		t.Fatalf("X-Niuniu-Mobile-ID: got %q, want %q", capturedMobileID, "mob-abc123")
	}
}

// TestHandshake_CipherCacheKeyMatchesLookup is a regression test for the double-
// namespace bug: handleHandshake used to store the cipher at "relay:"+h.MobileID
// while handleEncryptedRPC looks up at h.MobileID, producing a 412 "no_session"
// on every encrypted RPC after a successful handshake.
//
// The relay already namespaces the stream header mobile_id as "relay:<id>"
// (see relay/internal/api/proxy_handler.go). The desktop must store and look up
// under the exact same key the relay sent.
func TestHandshake_CipherCacheKeyMatchesLookup(t *testing.T) {
	desktopID, err := pairingcrypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity desktop: %v", err)
	}
	mobileID, err := pairingcrypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity mobile: %v", err)
	}

	proxy := NewProxyWithIdentity("http://127.0.0.1:0", desktopID)

	// Build the mobile-side initiator and produce msg1.
	initiator, err := pairingcrypto.NewKKInitiator(mobileID, desktopID.XPub)
	if err != nil {
		t.Fatalf("NewKKInitiator: %v", err)
	}
	msg1, err := initiator.WriteMessage(nil)
	if err != nil {
		t.Fatalf("initiator.WriteMessage: %v", err)
	}

	// The relay sends the stream header with mobile_id already namespaced.
	const namespacedMobileID = "relay:test-mobile-xyz"
	mobileXpubB64 := base64Encode(mobileID.XPub)
	streamHdr, _ := json.Marshal(streamReqHeader{
		StreamType:    "handshake",
		MobileID:      namespacedMobileID,
		MobileXpubB64: mobileXpubB64,
	})

	relayToProxyR, relayToProxyW := io.Pipe()
	proxyToRelayR, proxyToRelayW := io.Pipe()
	proxyStream := &pipeRWC{Reader: relayToProxyR, Writer: proxyToRelayW, closers: []io.Closer{relayToProxyR, proxyToRelayW}}
	relaySide := &pipeRWC{Reader: proxyToRelayR, Writer: relayToProxyW, closers: []io.Closer{proxyToRelayR, relayToProxyW}}

	go func() {
		_ = writeLenPrefix(relaySide, streamHdr)
		_, _ = relaySide.Write(msg1)
		_ = relayToProxyW.Close()
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		proxy.HandleStream(proxyStream)
	}()

	// Drain msg2 so HandleStream can return.
	if _, err := readLenPrefix(relaySide); err != nil {
		t.Fatalf("readLenPrefix response header: %v", err)
	}
	_, _ = io.ReadAll(relaySide)
	<-done

	// The cipher must be retrievable under the exact key the relay sent —
	// NOT under a re-namespaced "relay:"+h.MobileID, which was the bug.
	proxy.ciphersMu.Lock()
	_, okExact := proxy.ciphers[namespacedMobileID]
	_, okDoubled := proxy.ciphers["relay:"+namespacedMobileID]
	proxy.ciphersMu.Unlock()

	if okDoubled {
		t.Fatalf("cipher stored under double-prefixed key %q — regression of the 412 no_session bug",
			"relay:"+namespacedMobileID)
	}
	if !okExact {
		t.Fatalf("cipher not stored under %q; handleEncryptedRPC will return 412 no_session",
			namespacedMobileID)
	}
}

// base64Encode wraps stdlib base64 for readability in the test above.
func base64Encode(b []byte) string {
	return stdbase64.StdEncoding.EncodeToString(b)
}
