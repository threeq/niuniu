package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// LockfileInfo is the JSON body of ~/.niuniu/server.lock. The presence
// of this file (with a live PID) tells other niuniu processes that an
// active server is already running against the shared data directory.
type LockfileInfo struct {
	PID       int       `json:"pid"`
	Addr      string    `json:"addr"`
	Version   string    `json:"version"`
	StartedAt time.Time `json:"started_at"`
}

func writeLockfile(path string, info LockfileInfo) error {
	if info.StartedAt.IsZero() {
		info.StartedAt = time.Now().UTC()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	// os.Rename on Windows fails if dst exists; remove first (best-effort).
	_ = os.Remove(path)
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func removeLockfile(path string) error {
	err := os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
