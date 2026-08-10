// Package rotatinglog provides a date-suffixed io.Writer with retention-based
// cleanup. The writer is shared by the bundle subsystem (server stderr capture)
// and cmd/personal (its own diagnostic log), so both pipelines produce
// identically-named files under ~/.niuniu/logs/<base>-YYYY-MM-DD.<ext>.
package rotatinglog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Writer is an io.Writer that opens a date-suffixed log file under the same
// directory as a template path and rotates to a new file when the calendar
// day changes. Stale files older than retentionDays are pruned on each
// rotation. Safe for concurrent Write calls.
//
// The template's basename (without extension) is the rotation group's
// baseName. Cleanup matches files by HasPrefix(name, baseName+"-") +
// HasSuffix(name, ext); the baseName MUST NOT contain "-" or that prefix
// rule will collide with the date-suffix shape and cleanup will misbehave.
type Writer struct {
	mu            sync.Mutex
	dir           string
	baseName      string
	ext           string
	retentionDays int
	now           func() time.Time

	file        *os.File
	currentDate string
}

// New builds a Writer from a template path such as "/path/to/personal.log".
// Output files are named "personal-YYYY-MM-DD.log" in the same directory.
// retentionDays <= 0 disables cleanup.
//
// The basename portion of template (before extension) MUST NOT contain "-"
// because cleanup uses prefix matching to identify rotation-group files.
func New(template string, retentionDays int) *Writer {
	dir := filepath.Dir(template)
	fn := filepath.Base(template)
	ext := filepath.Ext(fn)
	base := strings.TrimSuffix(fn, ext)
	return &Writer{
		dir:           dir,
		baseName:      base,
		ext:           ext,
		retentionDays: retentionDays,
		now:           time.Now,
	}
}

func (w *Writer) fileName(date string) string {
	return filepath.Join(w.dir, fmt.Sprintf("%s-%s%s", w.baseName, date, w.ext))
}

// ensureOpen opens (or rotates to) the file matching the current date.
// Caller must hold w.mu.
func (w *Writer) ensureOpen() error {
	date := w.now().Format("2006-01-02")
	if w.file != nil && date == w.currentDate {
		return nil
	}
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(w.fileName(date), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	w.file = f
	w.currentDate = date
	w.cleanup()
	return nil
}

// Write implements io.Writer.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ensureOpen(); err != nil {
		return 0, err
	}
	return w.file.Write(p)
}

// Close closes the underlying file. Safe to call multiple times.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// cleanup prunes files in w.dir matching "<baseName>-*<ext>" whose mtime is
// older than retentionDays. retentionDays <= 0 disables cleanup. Errors are
// swallowed; cleanup must not block writes.
func (w *Writer) cleanup() {
	if w.retentionDays <= 0 {
		return
	}
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return
	}
	cutoff := w.now().AddDate(0, 0, -w.retentionDays)
	prefix := w.baseName + "-"
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, w.ext) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(w.dir, name))
		}
	}
}
