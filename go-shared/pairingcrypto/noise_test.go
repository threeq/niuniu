package pairingcrypto

import (
	"bytes"
	"testing"
)

func TestNoiseKKRoundTrip(t *testing.T) {
	mobile, _ := GenerateIdentity()
	desktop, _ := GenerateIdentity()

	init, err := NewKKInitiator(mobile, desktop.XPub)
	if err != nil {
		t.Fatalf("NewKKInitiator: %v", err)
	}
	resp, err := NewKKResponder(desktop, mobile.XPub)
	if err != nil {
		t.Fatalf("NewKKResponder: %v", err)
	}

	msg1, err := init.WriteMessage(nil)
	if err != nil {
		t.Fatalf("init.WriteMessage: %v", err)
	}
	if _, err := resp.ReadMessage(msg1); err != nil {
		t.Fatalf("resp.ReadMessage: %v", err)
	}
	msg2, err := resp.WriteMessage(nil)
	if err != nil {
		t.Fatalf("resp.WriteMessage: %v", err)
	}
	if _, err := init.ReadMessage(msg2); err != nil {
		t.Fatalf("init.ReadMessage: %v", err)
	}

	initCS, err := init.Cipher()
	if err != nil {
		t.Fatalf("init.Cipher: %v", err)
	}
	respCS, err := resp.Cipher()
	if err != nil {
		t.Fatalf("resp.Cipher: %v", err)
	}

	plain := []byte("hello relay")
	ct, err := initCS.Encrypt(nil, plain)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	got, err := respCS.Decrypt(nil, ct)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("plaintext mismatch: %q", got)
	}

	plain2 := []byte("hello mobile")
	ct2, _ := respCS.Encrypt(nil, plain2)
	got2, err := initCS.Decrypt(nil, ct2)
	if err != nil {
		t.Fatalf("reverse Decrypt: %v", err)
	}
	if !bytes.Equal(got2, plain2) {
		t.Fatalf("reverse mismatch")
	}

	initCS.Rekey()
	respCS.Rekey()
	ct3, _ := initCS.Encrypt(nil, plain)
	got3, err := respCS.Decrypt(nil, ct3)
	if err != nil {
		t.Fatalf("post-rekey Decrypt: %v", err)
	}
	if !bytes.Equal(got3, plain) {
		t.Fatalf("post-rekey mismatch")
	}
}

// newKKPair is a test helper that completes a full KK handshake and returns
// the two CipherPairs (initiator, responder).
func newKKPair(t *testing.T) (*CipherPair, *CipherPair) {
	t.Helper()
	mobile, _ := GenerateIdentity()
	desktop, _ := GenerateIdentity()
	init, _ := NewKKInitiator(mobile, desktop.XPub)
	resp, _ := NewKKResponder(desktop, mobile.XPub)
	msg1, _ := init.WriteMessage(nil)
	resp.ReadMessage(msg1)    //nolint
	msg2, _ := resp.WriteMessage(nil)
	init.ReadMessage(msg2)    //nolint
	iCS, _ := init.Cipher()
	rCS, _ := resp.Cipher()
	return iCS, rCS
}

func TestCipherPair_RekeyOnNthEncrypt(t *testing.T) {
	iCS, rCS := newKKPair(t)

	const rekeyAt = int64(3)
	iCS.SetRekeyInterval(rekeyAt)
	rCS.SetRekeyInterval(rekeyAt)

	// Send N messages crossing the threshold; they must all round-trip.
	for i := int64(0); i < rekeyAt*2; i++ {
		plain := []byte("test-frame")
		ct, err := iCS.Encrypt(nil, plain)
		if err != nil {
			t.Fatalf("Encrypt frame %d: %v", i, err)
		}
		got, err := rCS.Decrypt(nil, ct)
		if err != nil {
			t.Fatalf("Decrypt frame %d: %v", i, err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("frame %d: plaintext mismatch", i)
		}
	}
}

func TestCipherPair_RekeyIntervalZeroDisabled(t *testing.T) {
	iCS, rCS := newKKPair(t)
	// rekeyEvery = 0 (default) — counters should not trigger auto-rekey.
	if iCS.RekeyInterval() != 0 {
		t.Fatal("default rekeyEvery should be 0")
	}
	plain := []byte("hello")
	for i := 0; i < 10; i++ {
		ct, _ := iCS.Encrypt(nil, plain)
		if _, err := rCS.Decrypt(nil, ct); err != nil {
			t.Fatalf("Decrypt at i=%d: %v", i, err)
		}
	}
}

func TestCipherPair_CounterResetAfterAutoRekey(t *testing.T) {
	iCS, rCS := newKKPair(t)
	const rekeyAt = int64(2)
	iCS.SetRekeyInterval(rekeyAt)
	rCS.SetRekeyInterval(rekeyAt)

	// Encrypt exactly rekeyAt frames — triggers rekey and resets sendCount.
	plain := []byte("x")
	for i := int64(0); i < rekeyAt; i++ {
		ct, _ := iCS.Encrypt(nil, plain)
		rCS.Decrypt(nil, ct) //nolint
	}
	// Counter should have reset; next encrypt should succeed.
	ct, err := iCS.Encrypt(nil, plain)
	if err != nil {
		t.Fatalf("Encrypt after auto-rekey reset: %v", err)
	}
	if _, err := rCS.Decrypt(nil, ct); err != nil {
		t.Fatalf("Decrypt after auto-rekey reset: %v", err)
	}
}

func TestNoiseKKForgedMobileKeyFails(t *testing.T) {
	mobile, _ := GenerateIdentity()
	desktop, _ := GenerateIdentity()
	attacker, _ := GenerateIdentity()

	init, err := NewKKInitiator(attacker, desktop.XPub)
	if err != nil {
		t.Fatalf("NewKKInitiator: %v", err)
	}
	resp, err := NewKKResponder(desktop, mobile.XPub)
	if err != nil {
		t.Fatalf("NewKKResponder: %v", err)
	}
	msg1, _ := init.WriteMessage(nil)
	// responder may or may not accept msg1 depending on KK — some libs detect
	// the mismatch only at msg2. Confirm the full handshake fails.
	if _, rerr := resp.ReadMessage(msg1); rerr == nil {
		msg2, _ := resp.WriteMessage(nil)
		if _, err := init.ReadMessage(msg2); err == nil {
			// If handshake completes, cipher states won't decrypt each other
			// because peer static doesn't match. Confirm by trying an AEAD op.
			ics, _ := init.Cipher()
			rcs, _ := resp.Cipher()
			ct, _ := ics.Encrypt(nil, []byte("x"))
			if _, derr := rcs.Decrypt(nil, ct); derr == nil {
				t.Fatal("attacker succeeded end-to-end")
			}
		}
	}
}
