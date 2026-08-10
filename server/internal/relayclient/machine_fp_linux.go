//go:build linux

package relayclient

import (
	"os"
	"strings"
)

func platformFingerprint() string {
	b, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
