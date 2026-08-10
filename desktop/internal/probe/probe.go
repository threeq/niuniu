// Package probe determines whether niuniu-desktop can safely spawn
// its own embedded server against ~/.niuniu, and if not, whether it
// can reuse an already-running external server.
//
// The authoritative check is SQLite's exclusive-lock probe (dbIsBusy).
// Everything else (lockfile, port probe) is a best-effort way to find
// the addr of the holder so we can reuse rather than refuse.
package probe

import (
	"fmt"
	"path/filepath"
)

type RunningServer struct {
	Addr   string
	Source string // "lockfile" | "configport" | "defaultport"
}

type Decision struct {
	Reuse  *RunningServer
	Refuse string
}

// Decide inspects dataDir and returns a Decision. Any IO/syscall error
// bubbles up as `error`; Decision.Refuse is populated for "logical"
// refusals the user needs to see.
func Decide(dataDir string, personalVersion string) (Decision, error) {
	dbPath := filepath.Join(dataDir, "niuniu.db")

	busy, err := dbIsBusy(dbPath)
	if err != nil {
		return Decision{}, err
	}
	if !busy {
		return Decision{}, nil // DB is free — caller will spawn
	}

	// Another writer holds the DB. Find its addr.
	addr, source := findRunningServerAddr(dataDir)
	if addr == "" {
		return Decision{Refuse: "another niuniu process is using the database but its API cannot be reached. Quit the other process, or check ~/.niuniu/config.yaml for its port."}, nil
	}

	health, err := checkHealth(addr)
	if err != nil {
		return Decision{Refuse: fmt.Sprintf("found an external niuniu at %s but /api/health is unreachable: %v", addr, err)}, nil
	}
	if !versionCompatible(personalVersion, health.Version) {
		return Decision{Refuse: fmt.Sprintf("existing server version %s is incompatible with Personal %s — stop the external server or upgrade", health.Version, personalVersion)}, nil
	}
	return Decision{Reuse: &RunningServer{Addr: addr, Source: source}}, nil
}

// findRunningServerAddr tries three strategies in order to locate the
// addr of a server that is holding the SQLite lock. Returns empty on
// failure.
func findRunningServerAddr(dataDir string) (string, string) {
	if info, _ := readLockfile(dataDir); info != nil && pidAlive(info.PID) {
		if _, err := checkHealth(info.Addr); err == nil {
			return info.Addr, "lockfile"
		}
	}
	// Fall back to port configured in ~/.niuniu/config.yaml.
	if port := readConfiguredPort(dataDir); port > 0 {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		if _, err := checkHealth(addr); err == nil {
			return addr, "configport"
		}
	}
	// Last resort: default port.
	addr := "127.0.0.1:3000"
	if _, err := checkHealth(addr); err == nil {
		return addr, "defaultport"
	}
	return "", ""
}
