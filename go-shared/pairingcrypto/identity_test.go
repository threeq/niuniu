package pairingcrypto

import (
	"bytes"
	"testing"
)

func TestGenerateIdentityKeys(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if len(id.XPriv) != 32 || len(id.XPub) != 32 {
		t.Fatalf("X25519 sizes: priv=%d pub=%d", len(id.XPriv), len(id.XPub))
	}
	if len(id.EdPriv) != 64 || len(id.EdPub) != 32 {
		t.Fatalf("Ed25519 sizes: priv=%d pub=%d", len(id.EdPriv), len(id.EdPub))
	}
	if bytes.Equal(id.XPub, make([]byte, 32)) {
		t.Fatal("XPub is zero")
	}
}

func TestIdentityRoundTripEncoding(t *testing.T) {
	id, _ := GenerateIdentity()
	raw := id.MarshalBinary()
	if len(raw) != 160 {
		t.Fatalf("want 160 bytes, got %d", len(raw))
	}
	got, err := UnmarshalIdentity(raw)
	if err != nil {
		t.Fatalf("UnmarshalIdentity: %v", err)
	}
	if !bytes.Equal(id.XPriv, got.XPriv) || !bytes.Equal(id.EdPriv, got.EdPriv) {
		t.Fatal("round-trip mismatch")
	}
}

func TestIdentitySignVerify(t *testing.T) {
	id, _ := GenerateIdentity()
	msg := []byte("attestation payload")
	sig := id.Sign(msg)
	if !VerifyEd(id.EdPub, msg, sig) {
		t.Fatal("verify failed on self-signed")
	}
	if VerifyEd(id.EdPub, []byte("different"), sig) {
		t.Fatal("verify must fail on tampered message")
	}
}

func TestRandomBytesLength(t *testing.T) {
	b, err := RandomBytes(32)
	if err != nil {
		t.Fatalf("RandomBytes: %v", err)
	}
	if len(b) != 32 {
		t.Fatalf("len=%d", len(b))
	}
}
