package rotatinglog

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fixedClock returns a clock function that always returns t.
func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// mutableClock returns a clock function and a setter to change its value.
func mutableClock(initial time.Time) (func() time.Time, func(time.Time)) {
	var mu sync.Mutex
	cur := initial
	get := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return cur
	}
	set := func(t time.Time) {
		mu.Lock()
		defer mu.Unlock()
		cur = t
	}
	return get, set
}

func TestWriter_FirstWriteCreatesTodayFile(t *testing.T) {
	dir := t.TempDir()
	tmpl := filepath.Join(dir, "embedded-server.log")
	clock := fixedClock(time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC))

	w := New(tmpl, 30)
	w.now = clock
	defer w.Close()

	n, err := w.Write([]byte("hello\n"))
	require.NoError(t, err)
	require.Equal(t, 6, n)

	expected := filepath.Join(dir, "embedded-server-2026-05-06.log")
	data, err := os.ReadFile(expected)
	require.NoError(t, err)
	require.Equal(t, "hello\n", string(data))
}

func TestWriter_RotatesAtDayBoundary(t *testing.T) {
	dir := t.TempDir()
	tmpl := filepath.Join(dir, "embedded-server.log")
	day1 := time.Date(2026, 5, 6, 23, 59, 0, 0, time.UTC)
	day2 := time.Date(2026, 5, 7, 0, 0, 1, 0, time.UTC)
	get, set := mutableClock(day1)

	w := New(tmpl, 30)
	w.now = get
	defer w.Close()

	_, err := w.Write([]byte("before\n"))
	require.NoError(t, err)

	set(day2)
	_, err = w.Write([]byte("after\n"))
	require.NoError(t, err)

	day1File := filepath.Join(dir, "embedded-server-2026-05-06.log")
	day2File := filepath.Join(dir, "embedded-server-2026-05-07.log")

	got1, err := os.ReadFile(day1File)
	require.NoError(t, err)
	require.Equal(t, "before\n", string(got1))

	got2, err := os.ReadFile(day2File)
	require.NoError(t, err)
	require.Equal(t, "after\n", string(got2))
}

func TestWriter_PrunesFilesOlderThanRetention(t *testing.T) {
	dir := t.TempDir()
	tmpl := filepath.Join(dir, "embedded-server.log")
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)

	oldPath := filepath.Join(dir, "embedded-server-2026-04-01.log")
	require.NoError(t, os.WriteFile(oldPath, []byte("old"), 0o644))
	oldMtime := now.AddDate(0, 0, -35)
	require.NoError(t, os.Chtimes(oldPath, oldMtime, oldMtime))

	keepPath := filepath.Join(dir, "embedded-server-2026-05-05.log")
	require.NoError(t, os.WriteFile(keepPath, []byte("keep"), 0o644))
	keepMtime := now.AddDate(0, 0, -1)
	require.NoError(t, os.Chtimes(keepPath, keepMtime, keepMtime))

	w := New(tmpl, 30)
	w.now = fixedClock(now)
	defer w.Close()

	_, err := w.Write([]byte("trigger cleanup\n"))
	require.NoError(t, err)

	_, err = os.Stat(oldPath)
	require.True(t, os.IsNotExist(err), "old file should be pruned")

	_, err = os.Stat(keepPath)
	require.NoError(t, err, "recent file should be kept")
}

func TestWriter_DoesNotPruneUnrelatedFiles(t *testing.T) {
	dir := t.TempDir()
	tmpl := filepath.Join(dir, "embedded-server.log")
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	veryOld := now.AddDate(0, 0, -100)

	niuniu := filepath.Join(dir, "niuniu-2026-04-01.log")
	require.NoError(t, os.WriteFile(niuniu, []byte("x"), 0o644))
	require.NoError(t, os.Chtimes(niuniu, veryOld, veryOld))

	legacy := filepath.Join(dir, "embedded-server.log")
	require.NoError(t, os.WriteFile(legacy, []byte("x"), 0o644))
	require.NoError(t, os.Chtimes(legacy, veryOld, veryOld))

	w := New(tmpl, 30)
	w.now = fixedClock(now)
	defer w.Close()

	_, err := w.Write([]byte("trigger\n"))
	require.NoError(t, err)

	_, err = os.Stat(niuniu)
	require.NoError(t, err, "niuniu-*.log must be kept")
	_, err = os.Stat(legacy)
	require.NoError(t, err, "legacy embedded-server.log (no date) must be kept")
}

func TestWriter_RetentionZeroKeepsAll(t *testing.T) {
	dir := t.TempDir()
	tmpl := filepath.Join(dir, "embedded-server.log")
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)

	old := filepath.Join(dir, "embedded-server-2026-01-01.log")
	require.NoError(t, os.WriteFile(old, []byte("x"), 0o644))
	require.NoError(t, os.Chtimes(old, now.AddDate(0, 0, -125), now.AddDate(0, 0, -125)))

	w := New(tmpl, 0)
	w.now = fixedClock(now)
	defer w.Close()

	_, err := w.Write([]byte("trigger\n"))
	require.NoError(t, err)

	_, err = os.Stat(old)
	require.NoError(t, err, "retentionDays=0 must keep all files")
}

func TestWriter_ConcurrentWritesAreSerialized(t *testing.T) {
	dir := t.TempDir()
	tmpl := filepath.Join(dir, "embedded-server.log")
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)

	w := New(tmpl, 30)
	w.now = fixedClock(now)
	defer w.Close()

	const goroutines = 50
	const perGoroutine = 100
	payload := []byte("xxxxxxxxxx\n")

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				_, err := w.Write(payload)
				if err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	require.NoError(t, w.Close())

	got, err := os.ReadFile(filepath.Join(dir, "embedded-server-2026-05-06.log"))
	require.NoError(t, err)
	require.Equal(t, goroutines*perGoroutine*len(payload), len(got))
	require.Equal(t, goroutines*perGoroutine, strings.Count(string(got), "\n"))
}

func TestWriter_CloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	tmpl := filepath.Join(dir, "embedded-server.log")
	w := New(tmpl, 30)
	w.now = fixedClock(time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC))

	_, err := w.Write([]byte("hello\n"))
	require.NoError(t, err)

	require.NoError(t, w.Close())
	require.NoError(t, w.Close())
}

func TestWriter_CloseBeforeWriteIsNoop(t *testing.T) {
	dir := t.TempDir()
	tmpl := filepath.Join(dir, "embedded-server.log")
	w := New(tmpl, 30)
	require.NoError(t, w.Close())
}
