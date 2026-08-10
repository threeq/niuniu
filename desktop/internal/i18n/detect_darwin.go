//go:build darwin

package i18n

import (
	"os"
	"os/exec"
	"strings"
)

// osLocale reads the macOS user locale (e.g. "zh_CN", "en_US") via
// `defaults read -g AppleLocale`, falling back to the POSIX locale env vars.
func osLocale() string {
	if out, err := exec.Command("defaults", "read", "-g", "AppleLocale").Output(); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return s
		}
	}
	return envLocale()
}

func envLocale() string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}
