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
//  2. ~/.niuniu/bin/niuniu-agent(.exe) (manual install location);
//  3. "" → the caller falls back to the bare name "niuniu-agent", which the
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
	// 2. ~/.niuniu/bin.
	if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, ".niuniu", "bin", name)
		if fileExists(candidate) {
			return candidate
		}
	}
	return "" // bare name → PATH lookup
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
