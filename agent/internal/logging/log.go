package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// File logging for the agent process: the ACP server runs detached from any
// terminal (spawned by the niuniu server / desktop app), so its stderr — and
// with it every slog diagnostic — vanishes. InitFileLog redirects the default
// slog logger to a local file so turn hangs, model-client failures, and panics
// are always inspectable after the fact.
//
// Layout: ~/.niuniu-agent/logs/agent.log (append; rotated to agent.old.log at
// 10 MB). Override the path with NIUNIU_AGENT_LOG_FILE; disable with
// NIUNIU_AGENT_LOG_FILE=off.

const (
	maxLogBytes = 10 << 20
	logEnvFile  = "NIUNIU_AGENT_LOG_FILE"
)

var once sync.Once

// InitFileLog installs the file handler as the default slog destination.
// Best-effort: when the log file cannot be opened, stderr logging stays.
func InitFileLog(defaultDir string) {
	once.Do(func() {
		path := os.Getenv(logEnvFile)
		if path == "off" {
			return
		}
		if path == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return
			}
			if defaultDir != "" {
				defaultDir = filepath.Join(defaultDir, "logs")
			} else {
				defaultDir = filepath.Join(home, ".niuniu-agent", "logs")
			}
			path = filepath.Join(defaultDir, "agent.log")
		}
		f, err := openAppendRotate(path)
		if err != nil {
			return
		}
		// Dual-write: the file is the durable record (inspectable after a
		// hang/crash), stderr stays live for the terminal and for the host's
		// stderr bridge.
		w := io.MultiWriter(f, os.Stderr)
		slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})))
		slog.Info("logging initialized", "file", path)
	})
}

func openAppendRotate(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if info, err := os.Stat(path); err == nil && info.Size() > maxLogBytes {
		_ = os.Rename(path, path+".old")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log: %w", err)
	}
	return f, nil
}
