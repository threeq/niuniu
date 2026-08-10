package tokenbind

import (
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/go-shared/pairingcrypto"
)

func TestDPoPSignAndVerify(t *testing.T) {
	id, _ := pairingcrypto.GenerateIdentity()
	token := []byte("plain-device-token-for-test")
	now := time.Now().Unix()
	header := SignDPoP(id, token, "POST", "/d/desk-1/rpc", now)
	if err := VerifyDPoP(id.EdPub, token, "POST", "/d/desk-1/rpc", header, now, 60); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := VerifyDPoP(id.EdPub, token, "POST", "/wrong-path", header, now, 60); err == nil {
		t.Fatal("expected path mismatch to fail")
	}
	if err := VerifyDPoP(id.EdPub, token, "POST", "/d/desk-1/rpc", header, now+120, 60); err == nil {
		t.Fatal("expected stale timestamp to fail")
	}
	// Wrong token must fail.
	if err := VerifyDPoP(id.EdPub, []byte("wrong-token"), "POST", "/d/desk-1/rpc", header, now, 60); err == nil {
		t.Fatal("expected wrong token to fail")
	}
}

func TestTunnelChallenge(t *testing.T) {
	id, _ := pairingcrypto.GenerateIdentity()
	nonce, _ := pairingcrypto.RandomBytes(32)
	ts := time.Now().Unix()
	sig := SignTunnelChallenge(id, "desk-1", nonce, ts)
	if !VerifyTunnelChallenge(id.EdPub, "desk-1", nonce, ts, sig) {
		t.Fatal("verify failed on self-signed tunnel challenge")
	}
	if VerifyTunnelChallenge(id.EdPub, "other-desk", nonce, ts, sig) {
		t.Fatal("verify must fail for different desktop_id")
	}
}
