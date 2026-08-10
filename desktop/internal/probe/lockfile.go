package probe

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// LockfileInfo mirrors the JSON written by server/cmd/niuniu/lockfile.go.
// Additional fields are tolerated.
type LockfileInfo struct {
	PID     int    `json:"pid"`
	Addr    string `json:"addr"`
	Version string `json:"version"`
}

// readLockfile returns the parsed lockfile info, or nil if the file is
// missing or malformed. Liveness of the PID is the caller's concern.
func readLockfile(dataDir string) (*LockfileInfo, error) {
	path := filepath.Join(dataDir, "server.lock")
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var info LockfileInfo
	if err := json.Unmarshal(b, &info); err != nil {
		return nil, nil
	}
	if info.Addr == "" {
		return nil, nil
	}
	return &info, nil
}
