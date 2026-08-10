package logging

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// LogConfig holds the configuration for the logger.
type LogConfig struct {
	Output        string // "terminal", "file", or "both"
	Level         string // "debug", "info", "warn", "error"
	FileDir       string // log file directory, defaults to ~/.niuniu/logs
	RetentionDays int    // number of days to retain log files
}

// Setup configures the global logger based on the provided config.
func Setup(cfg LogConfig) error {
	// Validate output
	output := strings.ToLower(cfg.Output)
	if output != "terminal" && output != "file" && output != "both" {
		return fmt.Errorf("invalid output mode: %q (must be 'terminal', 'file', or 'both')", cfg.Output)
	}

	// Validate and parse level
	level, err := parseLevel(cfg.Level)
	if err != nil {
		return fmt.Errorf("invalid level: %w", err)
	}

	// Expand directory path
	dir := expandFileDir(cfg.FileDir)

	// Create handler based on output mode
	var handler slog.Handler

	switch output {
	case "terminal":
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	case "file":
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create log directory: %w", err)
		}
		handler = newRotatingFileHandler(dir, level, cfg.RetentionDays)
	case "both":
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create log directory: %w", err)
		}
		terminalHandler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
		fileHandler := newRotatingFileHandler(dir, level, cfg.RetentionDays)
		handler = newMultiHandler(terminalHandler, fileHandler)
	}

	slog.SetDefault(slog.New(handler))
	return nil
}

// parseLevel converts a string level to slog.Level.
func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("unknown level: %q", s)
	}
}

// expandFileDir returns the expanded directory path.
func expandFileDir(dir string) string {
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(os.Getenv("HOME"), ".niuniu", "logs")
		}
		return filepath.Join(home, ".niuniu", "logs")
	}
	return dir
}

// cleanupOldLogs removes log files older than maxAge days.
func cleanupOldLogs(dir string, maxAge int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	cutoff := time.Now().AddDate(0, 0, -maxAge)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
	return nil
}

// rotatingFileHandler implements slog.Handler for daily rotating log files.
type rotatingFileHandler struct {
	mu            sync.Mutex
	dir           string
	level         slog.Level
	retentionDays int
	currentDate   string
	file          *os.File
	handler       slog.Handler
}

// newRotatingFileHandler creates a new rotating file handler.
func newRotatingFileHandler(dir string, level slog.Level, retentionDays int) *rotatingFileHandler {
	return &rotatingFileHandler{
		dir:           dir,
		level:         level,
		retentionDays: retentionDays,
		currentDate:   time.Now().Format("2006-01-02"),
	}
}

// fileName returns the log file name for the given date.
func (h *rotatingFileHandler) fileName(date string) string {
	return filepath.Join(h.dir, fmt.Sprintf("niuniu-%s.log", date))
}

// openFile opens the log file for the current date, rotating if necessary.
func (h *rotatingFileHandler) openFile() error {
	date := time.Now().Format("2006-01-02")

	// Check if date changed or file not open
	if date == h.currentDate && h.file != nil {
		return nil
	}

	// Close existing file
	if h.file != nil {
		h.file.Close()
	}

	// Open new file
	f, err := os.OpenFile(h.fileName(date), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}

	h.file = f
	h.currentDate = date

	// Create underlying handler with the file
	h.handler = slog.NewTextHandler(f, &slog.HandlerOptions{Level: h.level})

	// Cleanup old logs
	cleanupOldLogs(h.dir, h.retentionDays)

	return nil
}

// Handle implements slog.Handler.
func (h *rotatingFileHandler) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if err := h.openFile(); err != nil {
		return err
	}

	return h.handler.Handle(ctx, r)
}

// Enabled implements slog.Handler.
func (h *rotatingFileHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= h.level
}

// WithAttrs implements slog.Handler.
func (h *rotatingFileHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.handler == nil {
		return h
	}
	return h.handler.WithAttrs(attrs)
}

// WithGroup implements slog.Handler.
func (h *rotatingFileHandler) WithGroup(name string) slog.Handler {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.handler == nil {
		return h
	}
	return h.handler.WithGroup(name)
}

// Close closes the underlying file handle.
func (h *rotatingFileHandler) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.file != nil {
		return h.file.Close()
	}
	return nil
}

// multiHandler implements slog.Handler by delegating to multiple handlers.
type multiHandler struct {
	handlers []slog.Handler
}

// newMultiHandler creates a new multi-handler.
func newMultiHandler(handlers ...slog.Handler) *multiHandler {
	return &multiHandler{handlers: handlers}
}

// Handle implements slog.Handler by calling all handlers.
func (h *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, handler := range h.handlers {
		if err := handler.Handle(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

// Enabled implements slog.Handler.
func (h *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

// WithAttrs implements slog.Handler.
func (h *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newHandlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		newHandlers[i] = handler.WithAttrs(attrs)
	}
	return &multiHandler{handlers: newHandlers}
}

// WithGroup implements slog.Handler.
func (h *multiHandler) WithGroup(name string) slog.Handler {
	newHandlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		newHandlers[i] = handler.WithGroup(name)
	}
	return &multiHandler{handlers: newHandlers}
}
