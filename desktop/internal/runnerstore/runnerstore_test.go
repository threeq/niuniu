package runnerstore

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s := New(filepath.Join(t.TempDir(), "runners.json"))
	// Deterministic clock so timestamps are assertable.
	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }
	return s
}

func TestUpsertCreatesThenUpdatesInPlace(t *testing.T) {
	s := newTestStore(t)

	r, err := s.Upsert(Runner{
		ConnectionKey: "10.0.0.5:3000", ConnectionName: "team", WorkspaceID: "42",
		WorkspaceName: "epic-1", LocalDir: "/tmp/a",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if r.ID == "" {
		t.Fatal("expected generated ID")
	}
	if r.Status != StatusActive {
		t.Fatalf("expected default status active, got %q", r.Status)
	}
	if got := s.List(); len(got) != 1 {
		t.Fatalf("expected 1 runner, got %d", len(got))
	}

	// Same (connKey, workspaceID) → update in place, keep ID.
	r2, err := s.Upsert(Runner{
		ConnectionKey: "10.0.0.5:3000", ConnectionName: "team-renamed", WorkspaceID: "42",
		WorkspaceName: "epic-1", LocalDir: "/tmp/b",
	})
	if err != nil {
		t.Fatalf("upsert 2: %v", err)
	}
	if r2.ID != r.ID {
		t.Fatalf("expected same ID on update, got %q vs %q", r2.ID, r.ID)
	}
	if r2.LocalDir != "/tmp/b" || r2.ConnectionName != "team-renamed" {
		t.Fatalf("expected updated fields, got %+v", r2)
	}
	if got := s.List(); len(got) != 1 {
		t.Fatalf("expected still 1 runner, got %d", len(got))
	}
}

func TestUpsertDistinctBindings(t *testing.T) {
	s := newTestStore(t)
	_, _ = s.Upsert(Runner{ConnectionKey: "h:1", WorkspaceID: "1"})
	_, _ = s.Upsert(Runner{ConnectionKey: "h:1", WorkspaceID: "2"})
	_, _ = s.Upsert(Runner{ConnectionKey: "h:2", WorkspaceID: "1"})
	if got := s.List(); len(got) != 3 {
		t.Fatalf("expected 3 distinct runners, got %d", len(got))
	}
}

func TestRemove(t *testing.T) {
	s := newTestStore(t)
	r, _ := s.Upsert(Runner{ConnectionKey: "h:1", WorkspaceID: "1"})
	_, _ = s.AppendLog(r.ID, LogEntry{Level: "stdout", Text: "hi"})

	ok, err := s.Remove(r.ID)
	if err != nil || !ok {
		t.Fatalf("remove: ok=%v err=%v", ok, err)
	}
	if got := s.List(); len(got) != 0 {
		t.Fatalf("expected 0 runners, got %d", len(got))
	}
	if logs := s.Logs(r.ID); len(logs) != 0 {
		t.Fatalf("expected logs cleared, got %d", len(logs))
	}
	if ok, _ := s.Remove("nope"); ok {
		t.Fatal("removing unknown id should report false")
	}
}

func TestSetStatus(t *testing.T) {
	s := newTestStore(t)
	r, _ := s.Upsert(Runner{ConnectionKey: "h:1", WorkspaceID: "1"})
	ok, err := s.SetStatus(r.ID, StatusStopped)
	if err != nil || !ok {
		t.Fatalf("setstatus: ok=%v err=%v", ok, err)
	}
	got, _ := s.Get(r.ID)
	if got.Status != StatusStopped {
		t.Fatalf("expected stopped, got %q", got.Status)
	}
	if ok, _ := s.SetStatus("nope", StatusActive); ok {
		t.Fatal("setstatus on unknown id should report false")
	}
}

func TestAppendLogBoundedAndSummary(t *testing.T) {
	s := newTestStore(t)
	r, _ := s.Upsert(Runner{ConnectionKey: "h:1", WorkspaceID: "1"})
	for range maxLogEntries + 50 {
		if ok, err := s.AppendLog(r.ID, LogEntry{Level: "stdout", Text: "line"}); err != nil || !ok {
			t.Fatalf("appendlog: ok=%v err=%v", ok, err)
		}
	}
	logs := s.Logs(r.ID)
	if len(logs) != maxLogEntries {
		t.Fatalf("expected log tail bounded to %d, got %d", maxLogEntries, len(logs))
	}
	got, _ := s.Get(r.ID)
	if got.LastLogSummary != "line" {
		t.Fatalf("expected summary set, got %q", got.LastLogSummary)
	}
	if ok, _ := s.AppendLog("nope", LogEntry{Text: "x"}); ok {
		t.Fatal("appendlog on unknown id should report false")
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runners.json")
	s := New(path)
	r, _ := s.Upsert(Runner{ConnectionKey: "h:1", ConnectionName: "n", WorkspaceID: "1", LocalDir: "/d"})
	_, _ = s.AppendLog(r.ID, LogEntry{Level: "command", Text: "go build"})

	// Reload into a fresh store.
	s2 := New(path)
	if err := s2.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	got := s2.List()
	if len(got) != 1 || got[0].ID != r.ID || got[0].LocalDir != "/d" {
		t.Fatalf("round-trip runners mismatch: %+v", got)
	}
	if logs := s2.Logs(r.ID); len(logs) != 1 || logs[0].Text != "go build" {
		t.Fatalf("round-trip logs mismatch: %+v", logs)
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err := s.Load(); err != nil {
		t.Fatalf("load missing: %v", err)
	}
	if got := s.List(); len(got) != 0 {
		t.Fatalf("expected empty, got %d", len(got))
	}
}
