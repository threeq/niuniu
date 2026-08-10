// Package lanproto defines the shared contract for desktop-mobile LAN discovery.
package lanproto

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
)

// ServiceName is the mDNS service type advertised by desktops willing to
// accept direct LAN mobile connections.
const ServiceName = "_niuniu-mobile._tcp"

// ProtocolVersion for the LAN discovery + tunnel contract.
const ProtocolVersion = 1

// TXTRecord captures the fields desktops put into their mDNS TXT record.
type TXTRecord struct {
	Version         int    // v (unsigned)
	DesktopID       string // did
	XpubFingerprint string // ifp - hex of sha256(xpub)[:16]
	Port            int    // port
}

// Encode produces the string slice suitable for mDNS TXT.
func (t *TXTRecord) Encode() []string {
	return []string{
		"v=" + strconv.Itoa(t.Version),
		"did=" + t.DesktopID,
		"ifp=" + t.XpubFingerprint,
		"port=" + strconv.Itoa(t.Port),
	}
}

// ParseTXT decodes a TXT record slice (as returned by hashicorp/mdns InfoFields).
// Unknown fields are ignored.
func ParseTXT(lines []string) (*TXTRecord, error) {
	out := &TXTRecord{}
	for _, line := range lines {
		k, v, ok := cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "v":
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, fmt.Errorf("lanproto: bad v: %w", err)
			}
			out.Version = n
		case "did":
			out.DesktopID = v
		case "ifp":
			out.XpubFingerprint = v
		case "port":
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, fmt.Errorf("lanproto: bad port: %w", err)
			}
			out.Port = n
		}
	}
	if out.Version == 0 || out.DesktopID == "" || out.XpubFingerprint == "" || out.Port == 0 {
		return nil, fmt.Errorf("lanproto: incomplete TXT record")
	}
	return out, nil
}

// FingerprintXpub returns hex(sha256(xpub))[:16].
func FingerprintXpub(xpub []byte) string {
	h := sha256.Sum256(xpub)
	return hex.EncodeToString(h[:8])
}

func cut(s, sep string) (before, after string, found bool) {
	for i := 0; i < len(s); i++ {
		if s[i:i+1] == sep {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}
