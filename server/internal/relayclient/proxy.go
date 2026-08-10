package relayclient

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/niuniu-dev/niuniu/go-shared/pairingcrypto"
)

// fingerprint returns a short hex-encoded prefix of the Noise handshake hash
// suitable for correlating handshake-complete and decrypt-failed log lines.
// 8 bytes (16 hex chars) is far below any collision threshold for this use.
func fingerprint(h []byte) string {
	if len(h) == 0 {
		return "nil"
	}
	n := 8
	if len(h) < n {
		n = len(h)
	}
	return hex.EncodeToString(h[:n])
}

// hexHead returns the hex encoding of the first n bytes of b (or fewer if b is
// shorter). Logging a ciphertext head lets us see whether bytes look like
// random AEAD output or something unexpected (truncated, ASCII, etc.).
func hexHead(b []byte, n int) string {
	if len(b) == 0 {
		return ""
	}
	if len(b) < n {
		n = len(b)
	}
	return hex.EncodeToString(b[:n])
}

// Proxy dispatches incoming yamux streams from the relay to the local niuniu
// HTTP / WebSocket server at LocalBaseURL.
//
// If Identity is set, the Proxy can also perform Noise KK handshakes and
// handle AEAD-encrypted RPC streams.  Without an Identity, only plaintext
// streams are supported (backward compat with existing tests).
type Proxy struct {
	// LocalBaseURL is the base URL of the local niuniu server, e.g. "http://127.0.0.1:3000".
	LocalBaseURL string

	// Identity is the desktop's long-term Noise KK / Ed25519 identity.
	// Required for handshake and encrypted-RPC support; may be nil for plaintext-only mode.
	Identity   *pairingcrypto.Identity
	httpClient *http.Client
	wsDialer   *websocket.Dialer

	ciphersMu sync.Mutex
	// ciphers maps mobile_id → established CipherPair after handshake completes.
	ciphers map[string]*pairingcrypto.CipherPair
}

// NewProxy returns a plaintext-only Proxy that forwards to localBaseURL.
// Plaintext RPC and WebSocket streams work; handshake/encrypted paths return errors.
func NewProxy(localBaseURL string) *Proxy {
	return &Proxy{
		LocalBaseURL: localBaseURL,
		httpClient:   &http.Client{},
		wsDialer:     websocket.DefaultDialer,
		ciphers:      map[string]*pairingcrypto.CipherPair{},
	}
}

// SetCipher stores a pre-established CipherPair for mobileID in the Proxy cipher
// cache. This is used by the LAN fast-path: after the desktop completes a KK
// handshake directly with the mobile (outside the relay), it calls SetCipher so
// subsequent encrypted_rpc streams over the same LAN session can find the cipher.
func (p *Proxy) SetCipher(mobileID string, cp *pairingcrypto.CipherPair) {
	p.ciphersMu.Lock()
	p.ciphers[mobileID] = cp
	p.ciphersMu.Unlock()
}

// RemoveCipher removes the CipherPair for mobileID from the cache. Subsequent
// encrypted_rpc streams for this mobile will receive a 412 and be forced to
// re-handshake (which will fail because the mobile is no longer trusted).
func (p *Proxy) RemoveCipher(mobileID string) {
	p.ciphersMu.Lock()
	delete(p.ciphers, mobileID)
	p.ciphersMu.Unlock()
}

// NewProxyWithIdentity returns a Proxy that supports Noise KK handshake and
// AEAD-encrypted RPC in addition to the plaintext paths.
func NewProxyWithIdentity(localBaseURL string, id *pairingcrypto.Identity) *Proxy {
	return &Proxy{
		LocalBaseURL: localBaseURL,
		Identity:     id,
		httpClient:   &http.Client{},
		wsDialer:     websocket.DefaultDialer,
		ciphers:      map[string]*pairingcrypto.CipherPair{},
	}
}

type streamReqHeader struct {
	Method        string              `json:"method"`
	Path          string              `json:"path"`
	StreamType    string              `json:"stream_type,omitempty"`
	Headers       map[string][]string `json:"headers"`
	MobileID      string              `json:"mobile_id,omitempty"`
	MobileXpubB64 string              `json:"mobile_xpub_b64,omitempty"`
	// PairKey is set on stream_type=pairkey_rpc. The relay reads the per-pair
	// AEAD key from desktop_mobile_links and forwards it inline so the desktop
	// can decrypt without persisting any keys.
	PairKey []byte `json:"pair_key,omitempty"`
}

type streamRespHeader struct {
	Status  int                 `json:"status"`
	Headers map[string][]string `json:"headers"`
}

func writeLenPrefix(w io.Writer, payload []byte) error {
	length := make([]byte, 4)
	binary.LittleEndian.PutUint32(length, uint32(len(payload)))
	if _, err := w.Write(length); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

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

// HandleStream reads one envelope from the stream, dispatches to the local
// niuniu server, and writes the response back to the stream.
func (p *Proxy) HandleStream(stream io.ReadWriteCloser) {
	defer stream.Close()
	hdrBytes, err := readLenPrefix(stream)
	if err != nil {
		return
	}
	var h streamReqHeader
	if err := json.Unmarshal(hdrBytes, &h); err != nil {
		return
	}
	switch h.StreamType {
	case "ws":
		p.handleWS(stream, h.Path)
	case "handshake":
		p.handleHandshake(stream, h)
	case "encrypted_rpc":
		p.handleEncryptedRPC(stream, h)
	case "pairkey_rpc":
		p.handlePairKeyRPC(stream, h)
	default:
		p.handleHTTP(stream, h)
	}
}

// handleHandshake performs the Noise KK responder side of the handshake.
//
// Protocol (desktop side):
//  1. Read msg1 bytes until EOF (relay sent FIN after msg1).
//  2. Build KKResponder using Identity + mobile's xpub from the stream header.
//  3. responder.ReadMessage(msg1) → responder.WriteMessage(nil) → msg2.
//  4. responder.Cipher() → cache the CipherPair keyed by mobile_id.
//  5. Write: len-prefixed {"status":200} header + raw msg2 bytes.
func (p *Proxy) handleHandshake(stream io.ReadWriteCloser, h streamReqHeader) {
	if p.Identity == nil {
		slog.Warn("relay handshake rejected: no identity configured", "mobile_id", h.MobileID)
		p.writeErr(stream, http.StatusServiceUnavailable, "no identity configured")
		return
	}
	mobileXpub, err := base64.StdEncoding.DecodeString(h.MobileXpubB64)
	if err != nil || len(mobileXpub) == 0 {
		slog.Warn("relay handshake rejected: bad mobile_xpub_b64",
			"mobile_id", h.MobileID, "xpub_b64_len", len(h.MobileXpubB64), "decode_err", err)
		p.writeErr(stream, http.StatusBadRequest, "missing or invalid mobile_xpub_b64")
		return
	}

	msg1, err := io.ReadAll(stream)
	if err != nil {
		slog.Warn("relay handshake: read msg1 failed", "mobile_id", h.MobileID, "err", err)
		p.writeErr(stream, http.StatusBadGateway, "read msg1: "+err.Error())
		return
	}

	responder, err := pairingcrypto.NewKKResponder(p.Identity, mobileXpub)
	if err != nil {
		slog.Warn("relay handshake: NewKKResponder failed", "mobile_id", h.MobileID, "err", err)
		p.writeErr(stream, http.StatusInternalServerError, "NewKKResponder: "+err.Error())
		return
	}
	if _, err := responder.ReadMessage(msg1); err != nil {
		// Most common cause: the mobile_xpub stored in the relay's DB does not match the
		// mobile's current x25519 keypair (identity rotated after pair). Re-pair to fix.
		slog.Warn("relay handshake: ReadMessage msg1 failed (likely mobile xpub mismatch)",
			"mobile_id", h.MobileID, "msg1_len", len(msg1), "xpub_len", len(mobileXpub), "err", err)
		p.writeErr(stream, http.StatusBadRequest, "ReadMessage msg1: "+err.Error())
		return
	}
	msg2, err := responder.WriteMessage(nil)
	if err != nil {
		p.writeErr(stream, http.StatusInternalServerError, "WriteMessage msg2: "+err.Error())
		return
	}
	cp, err := responder.Cipher()
	if err != nil {
		p.writeErr(stream, http.StatusInternalServerError, "Cipher: "+err.Error())
		return
	}
	// §7.5: rekey every 100,000 frames (≈100 MB at 1 KB avg) to bound key exposure.
	cp.SetRekeyInterval(100000)

	// Cache the cipher pair under the exact key the relay sent in the stream
	// header. The relay already namespaces it as "relay:<mobile_id>" (see
	// relay/internal/api/proxy_handler.go) to avoid collision with LAN
	// sessions keyed as "lan:<xpub_fingerprint>" (see lanhost/server.go).
	// handleEncryptedRPC does the symmetric lookup with the same key.
	if h.MobileID != "" {
		p.ciphersMu.Lock()
		prev, overwriting := p.ciphers[h.MobileID]
		p.ciphers[h.MobileID] = cp
		p.ciphersMu.Unlock()
		newFp := fingerprint(cp.HandshakeHash())
		if overwriting {
			slog.Warn("relay handshake: overwriting existing cipher (possible concurrent re-handshake)",
				"mobile_id", h.MobileID,
				"prev_fp", fingerprint(prev.HandshakeHash()),
				"new_fp", newFp)
		}
		slog.Info("relay handshake completed", "mobile_id", h.MobileID, "fp", newFp)

		NotifySessionOpen()
		defer NotifySessionClose()
	}

	// Write response: len-prefixed {"status":200} + raw msg2.
	rh, _ := json.Marshal(streamRespHeader{Status: http.StatusOK, Headers: map[string][]string{}})
	if err := writeLenPrefix(stream, rh); err != nil {
		return
	}
	_, _ = stream.Write(msg2)
}

// innerEnvelope is the plaintext structure that the mobile encrypts for an
// encrypted RPC request.
type innerEnvelope struct {
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Headers map[string][]string `json:"headers"`
	Body    []byte              `json:"body"`
}

// innerResponse is the plaintext structure that the desktop encrypts for an
// encrypted RPC response.
type innerResponse struct {
	Status  int                 `json:"status"`
	Headers map[string][]string `json:"headers"`
	Body    []byte              `json:"body"`
}

// handleEncryptedRPC decrypts the request body using the cached CipherPair for
// the sender, dispatches the plaintext HTTP request locally, then re-encrypts
// the response and sends it back.
//
// If no CipherPair is found for mobile_id the desktop responds with HTTP 412
// so the mobile knows to re-initiate the Noise KK handshake.
func (p *Proxy) handleEncryptedRPC(stream io.ReadWriteCloser, h streamReqHeader) {
	// Look up cipher by the full namespaced key.  The key is set as:
	//   relay path: "relay:<mobile_id>"    (by handleHandshake)
	//   LAN path:   "lan:<xpub_fingerprint>" (by lanhost/server.go)
	// Mobile clients send the namespaced key in the stream header mobile_id field.
	p.ciphersMu.Lock()
	cp := p.ciphers[h.MobileID]
	p.ciphersMu.Unlock()
	if cp == nil {
		slog.Warn("encrypted_rpc: no cipher session", "mobile_id", h.MobileID)
		p.writeErrWithHeaders(stream, http.StatusPreconditionFailed,
			map[string][]string{"X-Niuniu-Error": {"no_session"}},
			"no cipher session for mobile_id")
		return
	}

	ciphertext, err := io.ReadAll(stream)
	if err != nil {
		slog.Warn("encrypted_rpc: read ciphertext failed", "mobile_id", h.MobileID, "err", err)
		p.writeErr(stream, http.StatusBadGateway, "read ciphertext: "+err.Error())
		return
	}

	plaintext, err := cp.Decrypt(nil, ciphertext)
	if err != nil {
		slog.Warn("encrypted_rpc: decrypt failed",
			"mobile_id", h.MobileID,
			"cipher_fp", fingerprint(cp.HandshakeHash()),
			"ciphertext_len", len(ciphertext),
			"ciphertext_head", hexHead(ciphertext, 16),
			"err", err)
		p.writeErr(stream, http.StatusBadRequest, "decrypt: "+err.Error())
		return
	}

	var inner innerEnvelope
	if err := json.Unmarshal(plaintext, &inner); err != nil {
		slog.Warn("encrypted_rpc: unmarshal inner envelope failed",
			"mobile_id", h.MobileID, "plaintext_len", len(plaintext), "err", err)
		p.writeErr(stream, http.StatusBadRequest, "unmarshal inner envelope: "+err.Error())
		return
	}

	req, err := http.NewRequest(inner.Method, p.LocalBaseURL+inner.Path, bytes.NewReader(inner.Body))
	if err != nil {
		slog.Warn("encrypted_rpc: build request failed",
			"mobile_id", h.MobileID, "method", inner.Method, "path", inner.Path, "err", err)
		p.writeErr(stream, http.StatusBadRequest, "build request: "+err.Error())
		return
	}
	if u, err := url.Parse(p.LocalBaseURL); err == nil {
		req.Host = u.Host
		req.Header.Set("Origin", u.Scheme+"://"+u.Host)
	}
	for k, vs := range inner.Headers {
		if strings.EqualFold(k, "host") || strings.HasPrefix(strings.ToLower(k), "x-forwarded-") {
			continue
		}
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if h.MobileID != "" {
		req.Header.Set("X-Niuniu-Mobile-ID", h.MobileID)
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		p.writeErr(stream, http.StatusBadGateway, "local request: "+err.Error())
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	inner2, _ := json.Marshal(innerResponse{
		Status:  resp.StatusCode,
		Headers: resp.Header,
		Body:    respBody,
	})
	encResp, err := cp.Encrypt(nil, inner2)
	if err != nil {
		p.writeErr(stream, http.StatusInternalServerError, "encrypt response: "+err.Error())
		return
	}

	// Write outer response: len-prefixed {"status":200} + encrypted blob.
	rh, _ := json.Marshal(streamRespHeader{Status: http.StatusOK, Headers: map[string][]string{}})
	if err := writeLenPrefix(stream, rh); err != nil {
		return
	}
	_, _ = stream.Write(encResp)
}

// handlePairKeyRPC services a pairkey_rpc stream. Wire shape:
//
//	header  = streamReqHeader{StreamType:"pairkey_rpc", MobileID, PairKey,
//	                          Method, Path, Headers}
//	body    = nonce(12 bytes) || ciphertext(of the request body bytes)
//
// Request method/path/headers travel in the cleartext outer envelope (the
// relay needs them for routing and we already trust the relay with that
// metadata in the plaintext path). Only the request body content is
// encrypted. AAD = "pairkey-rpc/v1" || mobileID || method || path binds the
// ciphertext to its route so a replay against a different (method, path) or
// a different pair fails AEAD verification.
//
// Response: streamRespHeader carries the local server's status + headers
// verbatim (relay forwards them as the outer HTTP response); the encrypted
// body is `nonce(12) || ciphertext(of response body)` written to the stream.
// Concurrent and out-of-order frames are independent because every message
// carries its own random nonce — no monotonic counter.
func (p *Proxy) handlePairKeyRPC(stream io.ReadWriteCloser, h streamReqHeader) {
	// writePKErr — emit a desktop-side error in pairkey_rpc such that the
	// mobile can distinguish it from a normal AEAD-encrypted response. We
	// set X-Niuniu-Desktop-Error in the streamRespHeader.Headers; the
	// relay propagates those headers verbatim as the outer HTTP response
	// headers (see proxy_handler.go's `for k, vs := range rh.Headers`
	// loop), so the mobile's pairKeyFetch can short-circuit on that
	// header before attempting an AEAD-decrypt of the plain-text error
	// body — otherwise the AEAD failure swallows the original "decrypt:
	// …" or "build request: …" reason that pinpoints the bug.
	writePKErr := func(status int, code, msg string) {
		p.writeErrWithHeaders(stream, status, map[string][]string{
			"Content-Type":          {"text/plain; charset=utf-8"},
			"X-Niuniu-Desktop-Error": {code},
		}, msg)
	}

	if len(h.PairKey) != 32 {
		slog.Warn("pairkey_rpc: bad pair_key length", "mobile_id", h.MobileID, "len", len(h.PairKey))
		writePKErr(http.StatusBadRequest, "bad_pair_key", "pair_key length")
		return
	}
	wire, err := io.ReadAll(stream)
	if err != nil {
		writePKErr(http.StatusBadGateway, "read_body", "read body: "+err.Error())
		return
	}
	if len(wire) < pairKeyNonceLen+pairKeyTagLen {
		// Empty body is legal (GET requests etc.) but the AEAD tag still
		// needs to be present, so the minimum is nonce + 16-byte tag.
		writePKErr(http.StatusBadRequest, "envelope_too_short", "envelope too short")
		return
	}
	nonce := wire[:pairKeyNonceLen]
	ciphertext := wire[pairKeyNonceLen:]

	reqBody, err := pairKeyOpen(h.PairKey, nonce, ciphertext, pairKeyAAD(h.MobileID, h.Method, h.Path, false))
	if err != nil {
		slog.Warn("pairkey_rpc: decrypt failed",
			"mobile_id", h.MobileID,
			"method", h.Method,
			"path", h.Path,
			"ct_len", len(ciphertext),
			"err", err,
		)
		writePKErr(http.StatusBadRequest, "decrypt_failed", "decrypt: "+err.Error())
		return
	}

	// Build local request. Same hygiene as handleEncryptedRPC: rewrite Host
	// + Origin, drop forwarded headers, attach X-Niuniu-Mobile-ID.
	req, err := http.NewRequest(h.Method, p.LocalBaseURL+h.Path, bytes.NewReader(reqBody))
	if err != nil {
		writePKErr(http.StatusBadRequest, "build_request_failed", "build request: "+err.Error())
		return
	}
	if u, err := url.Parse(p.LocalBaseURL); err == nil {
		req.Host = u.Host
		req.Header.Set("Origin", u.Scheme+"://"+u.Host)
	}
	for k, vs := range h.Headers {
		if strings.EqualFold(k, "host") || strings.HasPrefix(strings.ToLower(k), "x-forwarded-") {
			continue
		}
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if h.MobileID != "" {
		req.Header.Set("X-Niuniu-Mobile-ID", h.MobileID)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		writePKErr(http.StatusBadGateway, "local_request_failed", "local request: "+err.Error())
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	respNonce, respCt, err := pairKeySeal(h.PairKey, respBody, pairKeyAAD(h.MobileID, h.Method, h.Path, true))
	if err != nil {
		writePKErr(http.StatusInternalServerError, "encrypt_response_failed", "encrypt response: "+err.Error())
		return
	}

	// Propagate the local server's status + headers as the outer response
	// (matches handleHTTP's plaintext semantics). Body bytes are encrypted,
	// so we MUST strip the cleartext body-framing headers — Content-Length
	// describes resp.Body's length, but we're sending nonce(12)+ciphertext
	// (= len(resp.Body)+28). A stale Content-Length makes Caddy in front of
	// the relay declare the wrong body length and abort the response with
	// "reading: unexpected EOF" the moment the byte count diverges; that
	// surfaces on the mobile as a generic "Network request failed" with no
	// breadcrumbs. The relay's RPC handler also strips these headers as
	// defence-in-depth (older desktops in the wild that don't have this
	// fix yet still work), but stripping at the origin keeps the wire
	// shape clean.
	respHeader := stripPairKeyResponseHeaders(resp.Header)
	rh, _ := json.Marshal(streamRespHeader{Status: resp.StatusCode, Headers: respHeader})
	if err := writeLenPrefix(stream, rh); err != nil {
		return
	}
	_, _ = stream.Write(respNonce)
	_, _ = stream.Write(respCt)
}

// stripPairKeyResponseHeaders returns a copy of in with the body-framing
// headers removed (Content-Length, Transfer-Encoding, Content-Encoding). The
// original map is not modified — callers may still need the cleartext
// Content-Length for billing or logging.
func stripPairKeyResponseHeaders(in http.Header) http.Header {
	if len(in) == 0 {
		return in
	}
	out := make(http.Header, len(in))
	for k, v := range in {
		switch http.CanonicalHeaderKey(k) {
		case "Content-Length", "Transfer-Encoding", "Content-Encoding":
			continue
		}
		out[k] = v
	}
	return out
}

func (p *Proxy) handleHTTP(stream io.ReadWriteCloser, h streamReqHeader) {
	body, _ := io.ReadAll(stream)
	req, err := http.NewRequest(h.Method, p.LocalBaseURL+h.Path, bytes.NewReader(body))
	if err != nil {
		p.writeErr(stream, http.StatusBadRequest, err.Error())
		return
	}
	// Rewrite Host and Origin to localhost so the local server's CORS sees a
	// same-origin request; strip forwarded headers (spec §11.1).
	if u, err := url.Parse(p.LocalBaseURL); err == nil {
		req.Host = u.Host
		req.Header.Set("Origin", u.Scheme+"://"+u.Host)
	}
	for k, vs := range h.Headers {
		if strings.EqualFold(k, "host") || strings.HasPrefix(strings.ToLower(k), "x-forwarded-") {
			continue
		}
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if h.MobileID != "" {
		req.Header.Set("X-Niuniu-Mobile-ID", h.MobileID)
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		p.writeErr(stream, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	rh, _ := json.Marshal(streamRespHeader{Status: resp.StatusCode, Headers: resp.Header})
	if err := writeLenPrefix(stream, rh); err != nil {
		return
	}
	_, _ = io.Copy(stream, resp.Body)
}

func (p *Proxy) handleWS(stream io.ReadWriteCloser, path string) {
	u, err := url.Parse(p.LocalBaseURL)
	if err != nil {
		p.writeErr(stream, http.StatusBadRequest, err.Error())
		return
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}
	u.Path = path
	ws, _, err := p.wsDialer.Dial(u.String(), nil)
	if err != nil {
		p.writeErr(stream, http.StatusBadGateway, err.Error())
		return
	}
	defer ws.Close()

	done := make(chan struct{}, 2)
	// ws -> stream
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if err := writeLenPrefix(stream, msg); err != nil {
				return
			}
		}
	}()
	// stream -> ws
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			msg, err := readLenPrefix(stream)
			if err != nil {
				return
			}
			if err := ws.WriteMessage(websocket.BinaryMessage, msg); err != nil {
				return
			}
		}
	}()
	<-done
}

func (p *Proxy) writeErr(stream io.Writer, status int, msg string) {
	p.writeErrWithHeaders(stream, status, map[string][]string{"Content-Type": {"text/plain; charset=utf-8"}}, msg)
}

func (p *Proxy) writeErrWithHeaders(stream io.Writer, status int, headers map[string][]string, msg string) {
	rh, _ := json.Marshal(streamRespHeader{
		Status:  status,
		Headers: headers,
	})
	if err := writeLenPrefix(stream, rh); err != nil {
		return
	}
	_, _ = stream.Write([]byte(fmt.Sprintf("relayclient: %s", msg)))
}
