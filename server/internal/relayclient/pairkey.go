package relayclient

import (
	cryptorand "crypto/rand"
	"errors"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

// Wire constants for the pairkey transport.
//
// The mobile and desktop must agree on these byte-for-byte; bumping any of
// them is a breaking change and requires a coordinated mobile build + relay
// + desktop rollout.
const (
	pairKeyNonceLen = chacha20poly1305.NonceSize    // 12
	pairKeyTagLen   = chacha20poly1305.Overhead     // 16
	pairKeyKeyLen   = chacha20poly1305.KeySize      // 32

	// pairKeyAADReqLabel / pairKeyAADRespLabel are domain-separation
	// prefixes mixed into the AEAD's associated data so a captured request
	// frame cannot be replayed as a response (or vice versa). Bumping
	// either is a wire-format break.
	pairKeyAADReqLabel  = "pairkey-rpc/v1"
	pairKeyAADRespLabel = "pairkey-rpc-resp/v1"
)

// pairKeyAAD constructs the AEAD associated data for the pairkey transport.
//
// Format:  label || 0x1f || mobileID || 0x1f || method || 0x1f || path
//
// `0x1f` (US — Unit Separator) is a non-printable ASCII byte that cannot
// appear inside any of the encoded fields (UUIDs, HTTP method names,
// URL-escaped paths). Including method+path binds the ciphertext to its
// route so a frame stolen for `GET /api/workspaces` cannot be re-played
// against `POST /api/workspaces/123/delete`.
func pairKeyAAD(mobileID, method, path string, response bool) []byte {
	label := pairKeyAADReqLabel
	if response {
		label = pairKeyAADRespLabel
	}
	const sep byte = 0x1f
	out := make([]byte, 0, len(label)+1+len(mobileID)+1+len(method)+1+len(path))
	out = append(out, label...)
	out = append(out, sep)
	out = append(out, mobileID...)
	out = append(out, sep)
	out = append(out, method...)
	out = append(out, sep)
	out = append(out, path...)
	return out
}

// pairKeySeal encrypts plaintext under key with a fresh random 96-bit nonce.
// Returns (nonce, ciphertext) — caller writes them concatenated on the wire.
//
// Random nonces (vs Noise's monotonic counter) are what makes this transport
// concurrency- and reorder-safe: every frame is independent.
func pairKeySeal(key, plaintext, ad []byte) (nonce, ciphertext []byte, err error) {
	if len(key) != pairKeyKeyLen {
		return nil, nil, fmt.Errorf("pairkey: bad key length %d (want %d)", len(key), pairKeyKeyLen)
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, pairKeyNonceLen)
	if _, err := cryptorand.Read(nonce); err != nil {
		return nil, nil, err
	}
	ciphertext = aead.Seal(nil, nonce, plaintext, ad)
	return nonce, ciphertext, nil
}

// pairKeyOpen verifies+decrypts a pairkey envelope. nonce must be the 12 bytes
// the sender prepended; ciphertext is the rest (Poly1305 tag is the trailing
// 16 bytes — chacha20poly1305 handles that internally). ad MUST match
// what the sender used or Open returns an authentication error.
func pairKeyOpen(key, nonce, ciphertext, ad []byte) ([]byte, error) {
	if len(key) != pairKeyKeyLen {
		return nil, fmt.Errorf("pairkey: bad key length %d", len(key))
	}
	if len(nonce) != pairKeyNonceLen {
		return nil, errors.New("pairkey: bad nonce length")
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, nonce, ciphertext, ad)
}
