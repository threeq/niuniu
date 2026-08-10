package lanproto

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestTXTRoundTrip(t *testing.T) {
	xpub := bytes.Repeat([]byte{0xAB}, 32)
	fp := FingerprintXpub(xpub)
	in := &TXTRecord{Version: 1, DesktopID: "desk-1", XpubFingerprint: fp, Port: 5353}
	lines := in.Encode()
	got, err := ParseTXT(lines)
	if err != nil {
		t.Fatalf("ParseTXT: %v", err)
	}
	if got.Version != 1 || got.DesktopID != "desk-1" || got.XpubFingerprint != fp || got.Port != 5353 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestFingerprintIsHex(t *testing.T) {
	fp := FingerprintXpub(bytes.Repeat([]byte{1}, 32))
	if len(fp) != 16 {
		t.Fatalf("expect 16 hex chars, got %d (%q)", len(fp), fp)
	}
	if _, err := hex.DecodeString(fp); err != nil {
		t.Fatalf("not hex: %v", err)
	}
}

func TestParseTXTRejectsIncomplete(t *testing.T) {
	if _, err := ParseTXT([]string{"v=1", "did=x"}); err == nil {
		t.Fatal("expected error on incomplete TXT")
	}
}
