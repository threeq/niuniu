//go:build !windows && !darwin

package i18n

import "os"

// osLocale reads the POSIX locale env vars (Linux and other Unix). Returns the
// first non-empty of LC_ALL, LC_MESSAGES, LANG (e.g. "zh_CN.UTF-8").
func osLocale() string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}
