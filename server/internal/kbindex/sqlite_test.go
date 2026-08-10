package kbindex

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func openTestIndex(t *testing.T) *SQLiteIndex {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kb_index.db")
	idx, err := OpenSQLiteIndex(path)
	if err != nil {
		t.Fatalf("OpenSQLiteIndex: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	return idx
}

func chunksOf(texts ...string) []Chunk {
	out := make([]Chunk, len(texts))
	off := 0
	for i, s := range texts {
		out[i] = Chunk{Index: i, Content: s, ByteOffset: off}
		off += len(s)
	}
	return out
}

func TestSQLiteIndexChineseMatch(t *testing.T) {
	idx := openTestIndex(t)
	ctx := context.Background()
	doc := IndexDoc{KBID: 1, DocumentID: 10, RelPath: "docs/cn.md",
		Chunks: chunksOf("知识库全文检索的中文段落", "无关内容段落")}
	if err := idx.IndexDocument(ctx, doc); err != nil {
		t.Fatalf("IndexDocument: %v", err)
	}
	hits, err := idx.Search(ctx, 1, "全文检索", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 chinese hit, got %d", len(hits))
	}
	h := hits[0]
	if h.RelPath != "docs/cn.md" || h.DocumentID != 10 || h.ChunkIndex != 0 {
		t.Fatalf("wrong pointer: %+v", h)
	}
	if !strings.Contains(h.Snippet, "检索") {
		t.Fatalf("snippet should contain the term, got %q", h.Snippet)
	}
}

func TestSQLiteIndexShortChineseFallback(t *testing.T) {
	// 2-char query is below the trigram minimum; LIKE fallback must still hit.
	idx := openTestIndex(t)
	ctx := context.Background()
	idx.IndexDocument(ctx, IndexDoc{KBID: 1, DocumentID: 10, RelPath: "a.md",
		Chunks: chunksOf("知识库检索系统")})
	hits, err := idx.Search(ctx, 1, "检索", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 short-query hit via fallback, got %d", len(hits))
	}
}

func TestSQLiteIndexScopedByKB(t *testing.T) {
	idx := openTestIndex(t)
	ctx := context.Background()
	idx.IndexDocument(ctx, IndexDoc{KBID: 1, DocumentID: 1, RelPath: "a.md",
		Chunks: chunksOf("alpha shared keyword beta")})
	idx.IndexDocument(ctx, IndexDoc{KBID: 2, DocumentID: 2, RelPath: "b.md",
		Chunks: chunksOf("gamma shared keyword delta")})
	hits, _ := idx.Search(ctx, 1, "keyword", 10)
	if len(hits) != 1 || hits[0].KBID != 1 {
		t.Fatalf("search must be scoped to kb 1, got %+v", hits)
	}
}

func TestSQLiteIndexReindexIdempotent(t *testing.T) {
	idx := openTestIndex(t)
	ctx := context.Background()
	idx.IndexDocument(ctx, IndexDoc{KBID: 1, DocumentID: 1, RelPath: "a.md",
		Chunks: chunksOf("first version content here", "second stale chunk gone")})
	// Re-index the same document with different content.
	idx.IndexDocument(ctx, IndexDoc{KBID: 1, DocumentID: 1, RelPath: "a.md",
		Chunks: chunksOf("third fresh content here")})
	if hits, _ := idx.Search(ctx, 1, "stale", 10); len(hits) != 0 {
		t.Fatalf("stale chunk should be gone after re-index, got %d", len(hits))
	}
	if hits, _ := idx.Search(ctx, 1, "fresh", 10); len(hits) != 1 {
		t.Fatalf("fresh content should be present, got %d", len(hits))
	}
}

func TestSQLiteIndexDeleteDocumentAndKB(t *testing.T) {
	idx := openTestIndex(t)
	ctx := context.Background()
	idx.IndexDocument(ctx, IndexDoc{KBID: 1, DocumentID: 1, RelPath: "a.md",
		Chunks: chunksOf("apple keyword one")})
	idx.IndexDocument(ctx, IndexDoc{KBID: 1, DocumentID: 2, RelPath: "b.md",
		Chunks: chunksOf("banana keyword two")})
	if err := idx.DeleteDocument(ctx, 1, 1); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}
	if hits, _ := idx.Search(ctx, 1, "keyword", 10); len(hits) != 1 || hits[0].DocumentID != 2 {
		t.Fatalf("only doc 2 should remain, got %+v", hits)
	}
	if err := idx.DeleteKB(ctx, 1); err != nil {
		t.Fatalf("DeleteKB: %v", err)
	}
	if hits, _ := idx.Search(ctx, 1, "keyword", 10); len(hits) != 0 {
		t.Fatalf("kb should be empty after DeleteKB, got %d", len(hits))
	}
}
