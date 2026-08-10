package relayclient

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
)

// TestPairKeyAAD_Shape pins the exact byte layout of the AEAD associated
// data so the desktop, mobile, and smoke harnesses cannot drift apart
// silently. If you change the AAD here you must update both
// `mobile/src/api/relay/pairKeyFetch.ts pairKeyAAD` and
// `relay/cmd/relay-tunnel-smoke/main.go smokePairKeyAAD` to match.
func TestPairKeyAAD_Shape(t *testing.T) {
	mobileID := "mob-1"
	method := "GET"
	path := "/api/ping"

	got := pairKeyAAD(mobileID, method, path, false)
	wantHex := hex.EncodeToString([]byte("pairkey-rpc/v1\x1fmob-1\x1fGET\x1f/api/ping"))
	if hex.EncodeToString(got) != wantHex {
		t.Fatalf("request AAD mismatch:\n got  %s\n want %s", hex.EncodeToString(got), wantHex)
	}

	gotResp := pairKeyAAD(mobileID, method, path, true)
	wantRespHex := hex.EncodeToString([]byte("pairkey-rpc-resp/v1\x1fmob-1\x1fGET\x1f/api/ping"))
	if hex.EncodeToString(gotResp) != wantRespHex {
		t.Fatalf("response AAD mismatch:\n got  %s\n want %s", hex.EncodeToString(gotResp), wantRespHex)
	}
	if !strings.HasPrefix(string(gotResp), pairKeyAADRespLabel) {
		t.Fatal("response AAD should start with the response label")
	}
}

func TestPairKey_RoundTrip(t *testing.T) {
	key := make([]byte, pairKeyKeyLen)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	pt := []byte("hello world from the mobile")
	ad := pairKeyAAD("mob-rt", "POST", "/api/echo", false)

	nonce, ct, err := pairKeySeal(key, pt, ad)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if len(nonce) != pairKeyNonceLen {
		t.Fatalf("nonce length %d", len(nonce))
	}
	if len(ct) != len(pt)+pairKeyTagLen {
		t.Fatalf("ciphertext length %d (want pt=%d + tag=%d)", len(ct), len(pt), pairKeyTagLen)
	}

	got, err := pairKeyOpen(key, nonce, ct, ad)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, pt) {
		t.Fatalf("plaintext mismatch:\n got  %q\n want %q", got, pt)
	}
}

// TestPairKey_AAD_Mismatch_RejectsDecrypt — any drift in mobile_id, method,
// path, or label causes Open to fail. This is the binding that prevents
// frame replay across routes or directions.
func TestPairKey_AAD_Mismatch_RejectsDecrypt(t *testing.T) {
	key := make([]byte, pairKeyKeyLen)
	_, _ = rand.Read(key)
	pt := []byte("payload")
	originalAD := pairKeyAAD("mob-A", "GET", "/api/x", false)
	nonce, ct, err := pairKeySeal(key, pt, originalAD)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		ad   []byte
	}{
		{"different mobile", pairKeyAAD("mob-B", "GET", "/api/x", false)},
		{"different method", pairKeyAAD("mob-A", "POST", "/api/x", false)},
		{"different path", pairKeyAAD("mob-A", "GET", "/api/y", false)},
		{"response direction", pairKeyAAD("mob-A", "GET", "/api/x", true)},
		{"empty AD", []byte{}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if _, err := pairKeyOpen(key, nonce, ct, tc.ad); err == nil {
				t.Fatalf("Open with AAD=%q must fail but succeeded", tc.name)
			}
		})
	}
}

// TestPairKey_NonceUniqueness — random 96-bit nonces should never collide
// in any reasonable run. This is just a smoke check that pairKeySeal is
// drawing from rand and not from a static counter (a regression mode that
// would silently break confidentiality on key reuse).
func TestPairKey_NonceUniqueness(t *testing.T) {
	key := make([]byte, pairKeyKeyLen)
	_, _ = rand.Read(key)
	ad := pairKeyAAD("mob", "GET", "/", false)
	seen := map[string]bool{}
	const N = 1000
	for i := 0; i < N; i++ {
		nonce, _, err := pairKeySeal(key, []byte("p"), ad)
		if err != nil {
			t.Fatal(err)
		}
		k := hex.EncodeToString(nonce)
		if seen[k] {
			t.Fatalf("nonce repeated after %d iterations: %s", i, k)
		}
		seen[k] = true
	}
}

// TestPairKey_BadKeyLength surfaces config errors loudly instead of
// silently producing a wrong cipher.
func TestPairKey_BadKeyLength(t *testing.T) {
	_, _, err := pairKeySeal(make([]byte, 31), []byte("x"), nil)
	if err == nil {
		t.Fatal("Seal with 31-byte key must fail")
	}
	_, err = pairKeyOpen(make([]byte, 33), make([]byte, 12), make([]byte, 16), nil)
	if err == nil {
		t.Fatal("Open with 33-byte key must fail")
	}
	_, err = pairKeyOpen(make([]byte, 32), make([]byte, 11), make([]byte, 16), nil)
	if err == nil {
		t.Fatal("Open with 11-byte nonce must fail")
	}
}
