package main

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveLogPath_PrefersUserHome(t *testing.T) {
	home := t.TempDir()
	r := pathResolver{
		userHome: func() (string, error) { return home, nil },
		exePath:  func() (string, error) { return filepath.Join(home, "bin", "x.exe"), nil },
		tempDir:  func() string { return t.TempDir() },
	}
	got := r.resolve()
	want := filepath.Join(home, ".niuniu", "logs", "personal.log")
	require.Equal(t, want, got)
}

func TestResolveLogPath_FallsBackToExeDirWhenHomeFails(t *testing.T) {
	exeDir := t.TempDir()
	r := pathResolver{
		userHome: func() (string, error) { return "", errors.New("USERPROFILE unset") },
		exePath:  func() (string, error) { return filepath.Join(exeDir, "personal.exe"), nil },
		tempDir:  func() string { return t.TempDir() },
	}
	got := r.resolve()
	want := filepath.Join(exeDir, ".niuniu", "logs", "personal.log")
	require.Equal(t, want, got, "must fall back to <exe-dir>/.niuniu/logs/personal.log")
}

func TestResolveLogPath_FallsBackToTempWhenHomeAndExeFail(t *testing.T) {
	tmp := t.TempDir()
	r := pathResolver{
		userHome: func() (string, error) { return "", errors.New("home failed") },
		exePath:  func() (string, error) { return "", errors.New("exe failed") },
		tempDir:  func() string { return tmp },
	}
	got := r.resolve()
	want := filepath.Join(tmp, "niuniu-desktop.log")
	require.Equal(t, want, got)
}

func TestSetupPersonalLog_WritesToFile(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, ".niuniu", "logs", "personal.log")

	prevDefault := slog.Default()
	defer slog.SetDefault(prevDefault)

	closer, err := installLogger(logPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = closer.Close() })

	slog.Info("probe entry", "k", "v")
	require.NoError(t, closer.Close())

	// rotatinglog rotates by date; pick whichever file appears in the dir.
	entries, err := os.ReadDir(filepath.Join(dir, ".niuniu", "logs"))
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	var data []byte
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "personal-") && strings.HasSuffix(e.Name(), ".log") {
			data, err = os.ReadFile(filepath.Join(dir, ".niuniu", "logs", e.Name()))
			require.NoError(t, err)
			break
		}
	}
	require.NotEmpty(t, data, "expected a personal-YYYY-MM-DD.log file with content")
	require.Contains(t, string(data), "probe entry")
	require.Contains(t, string(data), "k=v")
}

func TestSetupPersonalLog_FallsBackToStderrWhenFileUnwritable(t *testing.T) {
	// Point installLogger at a path under a parent that cannot be created.
	// On Windows, a path component that contains a NUL byte is universally
	// invalid; on Unix, a path under a non-directory works.
	var bad string
	if runtime.GOOS == "windows" {
		bad = "Z:\\__nonexistent_drive__\\nope\\personal.log"
	} else {
		// Use a path where a parent component is a regular file, not a dir.
		blocker := filepath.Join(t.TempDir(), "blocker")
		require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))
		bad = filepath.Join(blocker, "logs", "personal.log")
	}

	prevDefault := slog.Default()
	defer slog.SetDefault(prevDefault)

	closer, err := installLogger(bad)
	require.NoError(t, err, "must succeed by falling back, never propagate the IO error")
	t.Cleanup(func() { _ = closer.Close() })

	// Logging must still work even though the file backend is unavailable —
	// the slog default should have been swapped to a stderr-only handler.
	slog.Info("post-fallback line")
}
