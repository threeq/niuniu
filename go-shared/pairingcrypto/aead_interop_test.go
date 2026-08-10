package pairingcrypto

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"golang.org/x/crypto/chacha20poly1305"
)

// aeadVector is a single AEAD test vector for ChaCha20-Poly1305 interop.
type aeadVector struct {
	Key        string `json:"key"`
	Nonce      string `json:"nonce"`
	AD         string `json:"ad"`
	Plaintext  string `json:"plaintext"`
	Ciphertext string `json:"ciphertext"`
}

// TestGenerateAEADVectors generates aead_vectors.json when GEN_AEAD_VECTORS=1.
// The vectors use standard ChaCha20-Poly1305 (12-byte nonce), matching the
// cipher used inside the Noise KK CipherState.
func TestGenerateAEADVectors(t *testing.T) {
	if os.Getenv("GEN_AEAD_VECTORS") != "1" {
		t.Skip("set GEN_AEAD_VECTORS=1 to regenerate")
	}
	type input struct {
		key, nonce, ad, plaintext string
	}
	inputs := []input{
		{
			key:       "0000000000000000000000000000000000000000000000000000000000000000",
			nonce:     "000000000000000000000000",
			ad:        "",
			plaintext: "",
		},
		{
			key:       "0101010101010101010101010101010101010101010101010101010101010101",
			nonce:     "010101010101010101010101",
			ad:        "deadbeef",
			plaintext: "68656c6c6f",
		},
		{
			key:       "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f",
			nonce:     "0001020304050607080900000000",
			ad:        "01020304",
			plaintext: "48656c6c6f2c20576f726c6421",
		},
		{
			key:       "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
			nonce:     "ffffffffffffffffffffff00",
			ad:        "aabbccdd",
			plaintext: "6e69756e6975",
		},
		{
			key:       "cafebabecafebabecafebabecafebabecafebabecafebabecafebabecafebabe",
			nonce:     "deadbeefdeadbeefdeadbeef",
			ad:        "7465737420617373656369617465642064617461",
			plaintext: "6d6f62696c652d64657674657374",
		},
	}

	var vectors []aeadVector
	for _, in := range inputs {
		key, _ := hex.DecodeString(in.key)
		// Pad or truncate key to 32 bytes
		if len(key) < 32 {
			padded := make([]byte, 32)
			copy(padded, key)
			key = padded
		}
		key = key[:32]

		nonce, _ := hex.DecodeString(in.nonce)
		// Standard ChaCha20-Poly1305 uses 12-byte nonce
		if len(nonce) < 12 {
			padded := make([]byte, 12)
			copy(padded, nonce)
			nonce = padded
		}
		nonce = nonce[:12]

		ad, _ := hex.DecodeString(in.ad)
		plaintext, _ := hex.DecodeString(in.plaintext)

		aead, err := chacha20poly1305.New(key)
		if err != nil {
			t.Fatalf("chacha20poly1305.New: %v", err)
		}
		ct := aead.Seal(nil, nonce, plaintext, ad)

		vectors = append(vectors, aeadVector{
			Key:        hex.EncodeToString(key),
			Nonce:      hex.EncodeToString(nonce),
			AD:         hex.EncodeToString(ad),
			Plaintext:  hex.EncodeToString(plaintext),
			Ciphertext: hex.EncodeToString(ct),
		})
	}

	b, _ := json.MarshalIndent(vectors, "", "  ")
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile("testdata/aead_vectors.json", b, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("wrote %d vectors to testdata/aead_vectors.json", len(vectors))
}

// TestVerifyAEADVectors reads aead_vectors.json and verifies Go produces the same ciphertexts.
func TestVerifyAEADVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/aead_vectors.json")
	if err != nil {
		t.Skipf("testdata/aead_vectors.json not found: %v", err)
	}
	var vectors []aeadVector
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatalf("unmarshal vectors: %v", err)
	}
	for i, v := range vectors {
		key, _ := hex.DecodeString(v.Key)
		nonce, _ := hex.DecodeString(v.Nonce)
		ad, _ := hex.DecodeString(v.AD)
		plaintext, _ := hex.DecodeString(v.Plaintext)
		expectedCT, _ := hex.DecodeString(v.Ciphertext)

		aead, err := chacha20poly1305.New(key)
		if err != nil {
			t.Fatalf("vector %d: New: %v", i, err)
		}
		ct := aead.Seal(nil, nonce, plaintext, ad)
		if hex.EncodeToString(ct) != hex.EncodeToString(expectedCT) {
			t.Errorf("vector %d: ciphertext mismatch\n  got  %x\n  want %x", i, ct, expectedCT)
		}
		// Also verify round-trip decryption.
		pt, err := aead.Open(nil, nonce, ct, ad)
		if err != nil {
			t.Fatalf("vector %d: Open: %v", i, err)
		}
		if hex.EncodeToString(pt) != hex.EncodeToString(plaintext) {
			t.Errorf("vector %d: plaintext mismatch after decrypt", i)
		}
	}
}
