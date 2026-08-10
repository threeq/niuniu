package kbindex

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/pgtest"
)

// TestPostgresIndexChineseMatch verifies the Postgres-backed KBIndex returns
// Chinese substring hits, mirroring the SQLite sidecar test. It auto-skips when
// no Postgres is available (pgtest.SetupPGDB calls t.Skip), so it satisfies the
// dual-driver Chinese-hit acceptance wherever a live PG is present.
func TestPostgresIndexChineseMatch(t *testing.T) {
	rawPG, _ := pgtest.SetupPGDB(t) // t.Skip when Docker/PG unavailable
	idx, err := NewPostgresIndex(rawPG)
	if err != nil {
		t.Fatalf("NewPostgresIndex: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	ctx := context.Background()

	doc := IndexDoc{KBID: 1, DocumentID: 10, RelPath: "docs/cn.md",
		Chunks: chunksOf("知识库全文检索的中文段落", "无关内容")}
	if err := idx.IndexDocument(ctx, doc); err != nil {
		t.Fatalf("IndexDocument: %v", err)
	}
	// Re-index idempotency + scoping in one pass.
	if err := idx.IndexDocument(ctx, doc); err != nil {
		t.Fatalf("re-IndexDocument: %v", err)
	}
	hits, err := idx.Search(ctx, 1, "全文检索", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].RelPath != "docs/cn.md" {
		t.Fatalf("expected 1 chinese hit pointing at docs/cn.md, got %+v", hits)
	}
	// Cross-KB isolation.
	if other, _ := idx.Search(ctx, 99, "全文检索", 10); len(other) != 0 {
		t.Fatalf("kb 99 should have no hits, got %d", len(other))
	}
	if err := idx.DeleteKB(ctx, 1); err != nil {
		t.Fatalf("DeleteKB: %v", err)
	}
	if hits, _ := idx.Search(ctx, 1, "全文检索", 10); len(hits) != 0 {
		t.Fatalf("kb should be empty after DeleteKB, got %d", len(hits))
	}
}

// TestPostgresIndexRanking verifies the Postgres path relevance-ranks hits:
// a chunk that is essentially the query outranks a long chunk that merely
// contains it, and Score is emitted in the "lower = better" convention that
// SearchVisible sorts on (matching SQLite bm25). Auto-skips without a live PG.
func TestPostgresIndexRanking(t *testing.T) {
	rawPG, _ := pgtest.SetupPGDB(t) // t.Skip when Docker/PG unavailable
	idx, err := NewPostgresIndex(rawPG)
	if err != nil {
		t.Fatalf("NewPostgresIndex: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	if !idx.trgm {
		t.Skip("pg_trgm unavailable; ranking degrades to ILIKE order")
	}
	ctx := context.Background()

	const query = "postgres relevance ranking"
	// Both chunks contain the query substring (so both match ILIKE), but the
	// short chunk is almost entirely the query while the long chunk buries it in
	// unrelated text — similarity() length-normalizes, so the short one wins.
	strong := query
	weak := query + " is only a small aside inside a much longer document that " +
		"otherwise discusses many unrelated topics such as caching, replication, " +
		"vacuuming, connection pooling and assorted operational trivia"
	doc := IndexDoc{KBID: 1, DocumentID: 10, RelPath: "docs/rank.md",
		Chunks: chunksOf(weak, strong)} // weak first to prove order comes from Score, not insert order
	if err := idx.IndexDocument(ctx, doc); err != nil {
		t.Fatalf("IndexDocument: %v", err)
	}

	hits, err := idx.Search(ctx, 1, query, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d: %+v", len(hits), hits)
	}
	// Lower Score = better match: the strong (short) chunk must sort first.
	if hits[0].Score > hits[1].Score {
		t.Fatalf("hits not ranked ascending by Score: %+v", hits)
	}
	if hits[0].ChunkIndex != 1 {
		t.Fatalf("expected strong chunk (index 1) ranked first, got chunk %d (Score %v)",
			hits[0].ChunkIndex, hits[0].Score)
	}
	if !(hits[0].Score < 0) {
		t.Fatalf("expected negative Score for a real match, got %v", hits[0].Score)
	}
}
