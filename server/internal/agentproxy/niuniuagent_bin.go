package agentproxy

import (
	"os"
	"path/filepath"
	"runtime"
)

// resolveNiuniuAgentCommand fills in the default niuniu-agent command when
// config leaves it empty. Lookup order:
//
//  1. the directory of the running niuniu server executable (desktop sidecar
//     layout — the desktop app unpacks server/mcp/agent into one directory);
//  2. ~/.niuniu/desktop-v2/sidecars/niuniu-agent(.exe) — the desktop runtime
//     unpack directory, which the fingerprint mechanism keeps at the packaged
//     version on every desktop relaunch (Windows/Linux/macOS);
//  3. ~/.niuniu/bin/niuniu-agent(.exe) (manual install location);
//  4. "" → the caller falls back to the bare name "niuniu-agent", which the
//     OS resolves via PATH.
//
// An explicit config command always wins (checked by the caller).
func resolveNiuniuAgentCommand(configured string) string {
	if configured != "" {
		return configured
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	name := "niuniu-agent" + suffix

	// 1. Next to the running server executable (sidecar layout).
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), name)
		if fileExists(candidate) {
			return candidate
		}
	}
	// 2./3. Under ~/.niuniu: desktop unpack dir first (fingerprint-refreshed
	// on every desktop relaunch), then the manual install location.
	if home, err := os.UserHomeDir(); err == nil {
		candidates := []string{
			filepath.Join(home, ".niuniu", "desktop-v2", "sidecars", name),
			filepath.Join(home, ".niuniu", "bin", name),
		}
		for _, candidate := range candidates {
			if fileExists(candidate) {
				return candidate
			}
		}
	}
	return "" // bare name → PATH lookup
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
