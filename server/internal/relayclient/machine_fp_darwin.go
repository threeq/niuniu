//go:build darwin

package relayclient

import (
	"bytes"
	"os/exec"
	"strings"
)

func platformFingerprint() string {
	out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return ""
	}
	for _, line := range bytes.Split(out, []byte("\n")) {
		s := string(line)
		if strings.Contains(s, "IOPlatformUUID") {
			// line looks like: "IOPlatformUUID" = "XXXXXXXX-XXXX-XXXX-XXXX-XXXXXXXXXXXX"
			parts := strings.SplitN(s, "= ", 2)
			if len(parts) == 2 {
				val := strings.TrimSpace(parts[1])
				val = strings.Trim(val, `"`)
				if val != "" {
					return val
				}
			}
		}
	}
	return ""
}
