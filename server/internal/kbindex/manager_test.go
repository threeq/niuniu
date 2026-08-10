package kbindex

import (
	"context"
	"path/filepath"
	"testing"
)

func TestManagerSQLiteCachesByPath(t *testing.T) {
	dir := t.TempDir() // create (and register removal) before opening the index
	m := NewManager("sqlite", nil)
	t.Cleanup(func() { m.Close() })
	p := filepath.Join(dir, "kb_index.db")
	a, err := m.Get(p)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	b, err := m.Get(p)
	if err != nil {
		t.Fatalf("Get again: %v", err)
	}
	if a != b {
		t.Fatalf("same path should return the cached index instance")
	}
	// And it is actually usable.
	if err := a.IndexDocument(context.Background(), IndexDoc{
		KBID: 1, DocumentID: 1, RelPath: "x.md", Chunks: chunksOf("hello searchable world"),
	}); err != nil {
		t.Fatalf("IndexDocument: %v", err)
	}
	hits, _ := a.Search(context.Background(), 1, "searchable", 10)
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
}

func TestManagerSQLiteDistinctPaths(t *testing.T) {
	d1, d2 := t.TempDir(), t.TempDir()
	m := NewManager("sqlite", nil)
	t.Cleanup(func() { m.Close() })
	a, _ := m.Get(filepath.Join(d1, "kb_index.db"))
	b, _ := m.Get(filepath.Join(d2, "kb_index.db"))
	if a == b {
		t.Fatalf("distinct paths should return distinct indexes")
	}
}
