package logging

import (
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected slog.Level
		wantErr  bool
	}{
		{"debug lowercase", "debug", slog.LevelDebug, false},
		{"info lowercase", "info", slog.LevelInfo, false},
		{"warn lowercase", "warn", slog.LevelWarn, false},
		{"error lowercase", "error", slog.LevelError, false},
		{"DEBUG uppercase", "DEBUG", slog.LevelDebug, false},
		{"INFO uppercase", "INFO", slog.LevelInfo, false},
		{"WARN uppercase", "WARN", slog.LevelWarn, false},
		{"ERROR uppercase", "ERROR", slog.LevelError, false},
		{"Debug mixed case", "Debug", slog.LevelDebug, false},
		{"Info mixed case", "Info", slog.LevelInfo, false},
		{"Warn mixed case", "Warn", slog.LevelWarn, false},
		{"Error mixed case", "Error", slog.LevelError, false},
		{"invalid level", "trace", slog.LevelInfo, true},
		{"empty string", "", slog.LevelInfo, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseLevel(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseLevel() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.expected {
				t.Errorf("parseLevel() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestExpandFileDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("failed to get home dir: %v", err)
	}
	expectedDefault := filepath.Join(home, ".niuniu", "logs")

	tests := []struct {
		input    string
		expected string
	}{
		{"", expectedDefault},
		{"/custom/path", "/custom/path"},
		{"/var/log/niuniu", "/var/log/niuniu"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := expandFileDir(tt.input)
			if got != tt.expected {
				t.Errorf("expandFileDir(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestCleanupOldLogs(t *testing.T) {
	tmpDir := t.TempDir()

	// Create old log file (2 days ago)
	oldTime := time.Now().Add(-2 * 24 * time.Hour)
	oldFile := filepath.Join(tmpDir, "niuniu-2026-03-19.log")
	if err := os.WriteFile(oldFile, []byte("old log"), 0644); err != nil {
		t.Fatalf("failed to create old file: %v", err)
	}
	if err := os.Chtimes(oldFile, oldTime, oldTime); err != nil {
		t.Fatalf("failed to set old file time: %v", err)
	}

	// Create recent log file (today)
	recentFile := filepath.Join(tmpDir, time.Now().Format("niuniu-2006-01-02")+".log")
	if err := os.WriteFile(recentFile, []byte("recent log"), 0644); err != nil {
		t.Fatalf("failed to create recent file: %v", err)
	}

	// Run cleanup
	err := cleanupOldLogs(tmpDir, 1)
	if err != nil {
		t.Errorf("cleanupOldLogs() error = %v", err)
	}

	// Verify old file deleted
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Errorf("old file %q should be deleted", oldFile)
	}

	// Verify recent file kept
	if _, err := os.Stat(recentFile); os.IsNotExist(err) {
		t.Errorf("recent file %q should be kept", recentFile)
	}
}

func TestCleanupOldLogs_NoFiles(t *testing.T) {
	tmpDir := t.TempDir()

	err := cleanupOldLogs(tmpDir, 1)
	if err != nil {
		t.Errorf("cleanupOldLogs() on empty dir error = %v", err)
	}
}

func TestSetup_InvalidOutput(t *testing.T) {
	cfg := LogConfig{
		Output:  "invalid",
		Level:   "info",
		FileDir: t.TempDir(),
	}

	err := Setup(cfg)
	if err == nil {
		t.Error("Setup() expected error for invalid output, got nil")
	}
}

func TestSetup_InvalidLevel(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := LogConfig{
		Output:  "terminal",
		Level:   "invalid",
		FileDir: tmpDir,
	}

	err := Setup(cfg)
	if err == nil {
		t.Error("Setup() expected error for invalid level, got nil")
	}
}

func TestSetup_TerminalOutput(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := LogConfig{
		Output:        "terminal",
		Level:         "info",
		FileDir:       tmpDir,
		RetentionDays: 7,
	}

	err := Setup(cfg)
	if err != nil {
		t.Errorf("Setup() error = %v, want nil", err)
	}

	// Verify the default logger is set
	logger := slog.Default()
	if logger == nil {
		t.Error("slog.Default() returned nil after Setup")
	}

	// Write a log entry to verify it works
	logger.Info("terminal output test")
}

func TestSetup_FileOutput(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := LogConfig{
		Output:        "file",
		Level:         "info",
		FileDir:       tmpDir,
		RetentionDays: 7,
	}

	err := Setup(cfg)
	if err != nil {
		t.Errorf("Setup() error = %v, want nil", err)
	}

	// Write a log entry to trigger file creation
	slog.Info("file output test")

	// Verify a log file was created
	files, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Errorf("os.ReadDir() error = %v", err)
	}
	if len(files) == 0 {
		t.Error("expected log file to be created in file output mode")
	}

	// Reset logger to release file handles before temp dir cleanup
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	runtime.GC()
}

func TestSetup_BothOutput(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := LogConfig{
		Output:        "both",
		Level:         "info",
		FileDir:       tmpDir,
		RetentionDays: 7,
	}

	err := Setup(cfg)
	if err != nil {
		t.Errorf("Setup() error = %v, want nil", err)
	}

	// Write a log entry to trigger both outputs
	slog.Info("both output test")

	// Verify a log file was created
	files, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Errorf("os.ReadDir() error = %v", err)
	}
	if len(files) == 0 {
		t.Error("expected log file to be created in both output mode")
	}

	// Reset logger to release file handles before temp dir cleanup
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	runtime.GC()
}
