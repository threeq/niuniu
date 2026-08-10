package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"

	"github.com/niuniu-dev/niuniu-desktop/internal/rotatinglog"
)

// personalLogRetentionDays mirrors the embedded-server log retention so users
// don't end up with one log channel disappearing while the other accumulates.
const personalLogRetentionDays = 30

// pathResolver picks where personal's diagnostic log is written. Production
// callers use newPathResolver(); tests inject custom userHome/exePath/tempDir
// to verify each fallback rung.
type pathResolver struct {
	userHome func() (string, error)
	exePath  func() (string, error)
	tempDir  func() string
}

func newPathResolver() pathResolver {
	return pathResolver{
		userHome: os.UserHomeDir,
		exePath:  os.Executable,
		tempDir:  os.TempDir,
	}
}

// resolve returns the log file path. The three rungs are deliberate:
//
//  1. ~/.niuniu/logs/personal.log — the canonical location, matching
//     embedded-server.log.
//  2. <exe-dir>/.niuniu/logs/personal.log — covers the observed Windows case
//     where USERPROFILE is unset and defaultDataDir() falls back to a
//     cwd-relative ".niuniu" alongside the exe.
//  3. <temp>/niuniu-desktop.log — last resort so logging never blocks
//     startup.
func (r pathResolver) resolve() string {
	if home, err := r.userHome(); err == nil && home != "" {
		return filepath.Join(home, ".niuniu", "logs", "personal.log")
	}
	if exe, err := r.exePath(); err == nil && exe != "" {
		return filepath.Join(filepath.Dir(exe), ".niuniu", "logs", "personal.log")
	}
	return filepath.Join(r.tempDir(), "niuniu-desktop.log")
}

// resolveLogPath is the production entry point used by main().
func resolveLogPath() string { return newPathResolver().resolve() }

// installLogger wires slog.Default() to a date-rotating file under template,
// teeing to stderr so dev runs (`go run ./cmd/personal`) still see live output.
//
// If the file backend can't be opened (write probe fails), slog falls back to
// stderr-only — startup must never fail because of logging setup.
//
// Returns an io.Closer that the caller MUST defer-close to flush on shutdown.
func installLogger(template string) (io.Closer, error) {
	rw := rotatinglog.New(template, personalLogRetentionDays)

	// Probe the file backend with a zero-length write so we can fall back
	// gracefully when the path is unwritable (read-only volume, missing
	// drive, parent-not-a-directory, etc.).
	var sink io.Writer
	var closer io.Closer
	if _, err := rw.Write(nil); err != nil {
		_ = rw.Close()
		sink = os.Stderr
		closer = noopCloser{}
	} else {
		// Tee to stderr — harmless when stderr is detached (windowsgui).
		sink = io.MultiWriter(rw, os.Stderr)
		closer = rw
	}

	handler := slog.NewTextHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(handler))
	return closer, nil
}

// setupPersonalLog resolves the log path and installs the slog default.
// Returns the resolved path (for the startup-fingerprint line) and the closer.
func setupPersonalLog() (string, io.Closer) {
	path := resolveLogPath()
	closer, _ := installLogger(path) // installLogger never returns a non-nil error
	return path, closer
}

// recoverAndLog is intended for `defer recoverAndLog()` at the top of main().
// It writes the panic value and stack to slog (so the file log captures the
// crash) and then re-panics so the OS still sees the abnormal exit and
// records a Windows Error Reporting entry.
func recoverAndLog() {
	r := recover()
	if r == nil {
		return
	}
	slog.Error("personal panic",
		"recover", fmt.Sprintf("%v", r),
		"stack", string(debug.Stack()))
	panic(r)
}

type noopCloser struct{}

func (noopCloser) Close() error { return nil }
