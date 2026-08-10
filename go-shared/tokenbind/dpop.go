// Package tokenbind provides Ed25519 proof-of-possession helpers for bearer
// tokens (DPoP) and for the relay's /tunnel challenge.
package tokenbind

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/niuniu-dev/niuniu/go-shared/pairingcrypto"
)

// SignDPoP returns the value of the X-DPoP header.
//
// Format: base64url(sig) || "." || unix_seconds
//
// Per spec §7.4: sig_input = Ed25519.Sign(sk_mobile, HMAC-SHA256(deviceTokenPlain, method+" "+path+" "+unix_seconds))
// The HMAC key is the plaintext device token — known to the mobile that holds it
// and re-derivable by the server from the presented Bearer token.
func SignDPoP(id *pairingcrypto.Identity, deviceTokenPlain []byte, method, path string, unixSeconds int64) string {
	digest := dpopHMAC(deviceTokenPlain, method, path, unixSeconds)
	sig := id.Sign(digest)
	return base64.RawURLEncoding.EncodeToString(sig) + "." + strconv.FormatInt(unixSeconds, 10)
}

// VerifyDPoP verifies the header against (edPub, deviceTokenPlain, method, path) and enforces a
// freshness window (|nowUnix - signed_ts| <= skewSeconds).
//
// deviceTokenPlain is the raw plaintext device token extracted from the Bearer header —
// the server always has it at verify time.
func VerifyDPoP(edPub []byte, deviceTokenPlain []byte, method, path, header string, nowUnix, skewSeconds int64) error {
	parts := strings.Split(header, ".")
	if len(parts) != 2 {
		return errors.New("tokenbind: malformed DPoP header")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return fmt.Errorf("tokenbind: bad base64: %w", err)
	}
	ts, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return fmt.Errorf("tokenbind: bad timestamp: %w", err)
	}
	if d := nowUnix - ts; d < -skewSeconds || d > skewSeconds {
		return fmt.Errorf("tokenbind: timestamp skew %d exceeds %d", d, skewSeconds)
	}
	digest := dpopHMAC(deviceTokenPlain, method, path, ts)
	if !pairingcrypto.VerifyEd(edPub, digest, sig) {
		return errors.New("tokenbind: bad signature")
	}
	return nil
}

// dpopHMAC computes HMAC-SHA256(key=deviceTokenPlain, data=method+" "+path+" "+ts).
func dpopHMAC(deviceTokenPlain []byte, method, path string, ts int64) []byte {
	mac := hmac.New(sha256.New, deviceTokenPlain)
	mac.Write([]byte(method + " " + path + " " + strconv.FormatInt(ts, 10)))
	return mac.Sum(nil)
}

// SignTunnelChallenge returns the desktop's response to the relay's /tunnel challenge.
// sig_input = "niuniu-tunnel-v1" || desktop_id || nonce || ts (decimal)
func SignTunnelChallenge(id *pairingcrypto.Identity, desktopID string, nonce []byte, ts int64) []byte {
	return id.Sign(tunnelInput(desktopID, nonce, ts))
}

// VerifyTunnelChallenge verifies a desktop's tunnel challenge signature.
func VerifyTunnelChallenge(edPub []byte, desktopID string, nonce []byte, ts int64, sig []byte) bool {
	return pairingcrypto.VerifyEd(edPub, tunnelInput(desktopID, nonce, ts), sig)
}

func tunnelInput(desktopID string, nonce []byte, ts int64) []byte {
	var input []byte
	input = append(input, []byte("niuniu-tunnel-v1")...)
	input = append(input, []byte(desktopID)...)
	input = append(input, nonce...)
	input = append(input, []byte(strconv.FormatInt(ts, 10))...)
	return input
}
