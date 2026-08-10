// Package pairingcrypto implements cryptographic primitives used during
// pairing and runtime E2E sessions between a paired mobile and desktop.
package pairingcrypto

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
)

// Identity holds a party's long-term X25519 (Noise KK) and Ed25519
// (tunnel PoP + DPoP) identity keys.
type Identity struct {
	XPriv  []byte // 32 bytes
	XPub   []byte // 32 bytes
	EdPriv []byte // 64 bytes (seed || pub)
	EdPub  []byte // 32 bytes
}

// GenerateIdentity creates a fresh Identity from crypto/rand.
func GenerateIdentity() (*Identity, error) {
	xk, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	edPub, edPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Identity{
		XPriv:  xk.Bytes(),
		XPub:   xk.PublicKey().Bytes(),
		EdPriv: edPriv,
		EdPub:  edPub,
	}, nil
}

// MarshalBinary returns a compact 32+32+64+32 = 160-byte representation.
// Callers encrypt this blob before writing to disk.
func (id *Identity) MarshalBinary() []byte {
	out := make([]byte, 0, 160)
	out = append(out, id.XPriv...)
	out = append(out, id.XPub...)
	out = append(out, id.EdPriv...)
	out = append(out, id.EdPub...)
	return out
}

// UnmarshalIdentity parses a 160-byte blob produced by MarshalBinary.
func UnmarshalIdentity(b []byte) (*Identity, error) {
	if len(b) != 160 {
		return nil, errors.New("pairingcrypto: identity blob must be 160 bytes")
	}
	return &Identity{
		XPriv:  append([]byte(nil), b[0:32]...),
		XPub:   append([]byte(nil), b[32:64]...),
		EdPriv: append([]byte(nil), b[64:128]...),
		EdPub:  append([]byte(nil), b[128:160]...),
	}, nil
}

// Sign signs msg with the identity's Ed25519 private key.
func (id *Identity) Sign(msg []byte) []byte {
	return ed25519.Sign(id.EdPriv, msg)
}

// VerifyEd verifies sig against msg using pub (Ed25519 public key).
func VerifyEd(pub, msg, sig []byte) bool {
	if len(pub) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), msg, sig)
}

// RandomBytes returns n bytes from rand.Reader.
func RandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return nil, err
	}
	return b, nil
}
