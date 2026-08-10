// Package kbindex implements the knowledge-base full-text index. It is a
// deliberately isolated component: on SQLite the index lives in a per-owner
// sidecar database (kb_index.db) with its own connection, WAL and lock domain,
// and is NOT routed through the main store's dual-driver wrapper and NOT part
// of dual-schema parity. On Postgres the index is backed by tsvector/pg_trgm.
// Both engines sit behind the KBIndex interface so the choice never leaks into
// the KB service. The index stores only pointers (rel_path + byte offset) plus
// the searchable chunk text; full documents stay on disk for direct reads and
// the index can always be rebuilt from source.
package kbindex

import "context"

// SearchHit is a single full-text match: a pointer back into the source
// document plus a human-readable snippet and a relevance score.
type SearchHit struct {
	KBID       int64   `json:"kb_id"`
	DocumentID int64   `json:"document_id"`
	ChunkIndex int     `json:"chunk_index"`
	RelPath    string  `json:"rel_path"`
	ByteOffset int     `json:"byte_offset"`
	Snippet    string  `json:"snippet"`
	Score      float64 `json:"score"`
}

// IndexDoc bundles one document's chunks for (re-)indexing.
type IndexDoc struct {
	KBID       int64
	DocumentID int64
	RelPath    string
	Chunks     []Chunk
}

// KBIndex is the full-text index abstraction over KB chunks.
type KBIndex interface {
	// IndexDocument atomically replaces every chunk previously indexed for
	// (KBID, DocumentID) with doc.Chunks. Idempotent: safe to re-run on
	// unchanged or changed documents.
	IndexDocument(ctx context.Context, doc IndexDoc) error
	// DeleteDocument removes all chunks for one document.
	DeleteDocument(ctx context.Context, kbID, documentID int64) error
	// DeleteKB removes all chunks for an entire knowledge base.
	DeleteKB(ctx context.Context, kbID int64) error
	// Search returns up to limit ranked hits for query within one KB.
	Search(ctx context.Context, kbID int64, query string, limit int) ([]SearchHit, error)
	// Close releases the underlying handle.
	Close() error
}
