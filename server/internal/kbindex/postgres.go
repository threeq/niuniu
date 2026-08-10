package kbindex

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
)

// PostgresIndex is the Postgres-backed KB full-text index. Postgres uses MVCC
// (no single-writer lock), so the index table lives in the main database and
// shares its connection pool without the contention concerns that drive the
// SQLite sidecar. The kb_chunks table is self-managed by this package (created
// on open) and is intentionally NOT part of dual-schema parity, mirroring the
// SQLite sidecar: it is a kbindex-private component on both engines.
//
// Substring/CJK matching is provided by the pg_trgm extension (GIN trigram
// index over content), the Postgres analogue of SQLite's trigram tokenizer.
// pg_trgm needs no language-specific dictionary, so Chinese works without
// zhparser/pg_jieba. If the extension cannot be created (insufficient
// privileges) the ILIKE filter still returns correct results via a sequential
// scan; only the acceleration is lost.
type PostgresIndex struct {
	db *sql.DB
	// trgm records whether the pg_trgm extension is available. When true, Search
	// ranks hits by similarity(content, query) (the length-normalized analogue of
	// SQLite's bm25); when false it degrades to the unranked ILIKE order.
	trgm bool
}

// NewPostgresIndex wraps an existing Postgres *sql.DB (pgx stdlib) and ensures
// the index schema. The caller retains ownership of db; Close is a no-op.
func NewPostgresIndex(db *sql.DB) (*PostgresIndex, error) {
	p := &PostgresIndex{db: db}
	if err := p.ensureSchema(context.Background()); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *PostgresIndex) ensureSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS kb_chunks (
			id          BIGSERIAL PRIMARY KEY,
			kb_id       BIGINT NOT NULL,
			document_id BIGINT NOT NULL,
			chunk_index INTEGER NOT NULL,
			rel_path    TEXT NOT NULL,
			byte_offset BIGINT NOT NULL,
			content     TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_kb_chunks_doc ON kb_chunks(kb_id, document_id)`,
	}
	for _, q := range stmts {
		if _, err := p.db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("kbindex(pg): ensure schema: %w", err)
		}
	}
	// pg_trgm + GIN index are best-effort: degrade to seq-scan ILIKE if the
	// extension is unavailable rather than failing index startup.
	if _, err := p.db.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS pg_trgm`); err != nil {
		slog.Warn("kbindex(pg): pg_trgm unavailable; substring search will not be index-accelerated or relevance-ranked", "err", err)
		return nil
	}
	// pg_trgm present (created or pre-existing): similarity() is now callable, so
	// Search can rank hits instead of returning them in raw table order.
	p.trgm = true
	if _, err := p.db.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_kb_chunks_trgm ON kb_chunks USING gin (content gin_trgm_ops)`); err != nil {
		slog.Warn("kbindex(pg): trigram GIN index creation failed", "err", err)
	}
	return nil
}

// IndexDocument replaces all chunks for (KBID, DocumentID) in one transaction.
func (p *PostgresIndex) IndexDocument(ctx context.Context, doc IndexDoc) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM kb_chunks WHERE kb_id = $1 AND document_id = $2`,
		doc.KBID, doc.DocumentID); err != nil {
		return err
	}
	for _, c := range doc.Chunks {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO kb_chunks(kb_id, document_id, chunk_index, rel_path, byte_offset, content)
			 VALUES($1, $2, $3, $4, $5, $6)`,
			doc.KBID, doc.DocumentID, c.Index, doc.RelPath, c.ByteOffset, c.Content); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteDocument removes all chunks for one document.
func (p *PostgresIndex) DeleteDocument(ctx context.Context, kbID, documentID int64) error {
	_, err := p.db.ExecContext(ctx,
		`DELETE FROM kb_chunks WHERE kb_id = $1 AND document_id = $2`, kbID, documentID)
	return err
}

// DeleteKB removes all chunks for an entire knowledge base.
func (p *PostgresIndex) DeleteKB(ctx context.Context, kbID int64) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM kb_chunks WHERE kb_id = $1`, kbID)
	return err
}

// Search returns up to limit hits within one KB using a trigram-accelerated
// ILIKE substring match (correct for Chinese and Latin text alike). When
// pg_trgm is available the hits are relevance-ranked; otherwise they fall back
// to raw table order.
func (p *PostgresIndex) Search(ctx context.Context, kbID int64, query string, limit int) ([]SearchHit, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
	like := "%" + esc + "%"
	if p.trgm {
		return p.searchRanked(ctx, kbID, q, like, limit)
	}
	return p.searchLike(ctx, kbID, q, like, limit)
}

// searchRanked filters by ILIKE substring (index-accelerated by the trigram GIN
// index) and orders by similarity(content, query). pg_trgm similarity is in
// [0,1] with higher = more similar, and is length-normalized (a chunk that is
// mostly the query scores higher than a long chunk that merely contains it) —
// the same intent as SQLite's bm25 length normalization. We store Score as the
// negated similarity so the "lower Score = better match" convention that
// SearchVisible sorts on holds for both drivers (bm25 is most-negative-best).
func (p *PostgresIndex) searchRanked(ctx context.Context, kbID int64, q, like string, limit int) ([]SearchHit, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT document_id, chunk_index, rel_path, byte_offset, content,
		        similarity(content, $2) AS sim
		 FROM kb_chunks
		 WHERE kb_id = $1 AND content ILIKE $3
		 ORDER BY sim DESC, document_id, chunk_index
		 LIMIT $4`, kbID, q, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SearchHit
	for rows.Next() {
		var h SearchHit
		var content string
		var sim float64
		if err := rows.Scan(&h.DocumentID, &h.ChunkIndex, &h.RelPath, &h.ByteOffset, &content, &sim); err != nil {
			return nil, err
		}
		h.KBID = kbID
		h.Score = -sim
		h.Snippet = makeSnippet(content, q)
		out = append(out, h)
	}
	return out, rows.Err()
}

// searchLike is the pg_trgm-unavailable fallback: correct ILIKE substring
// matching (via sequential scan) with no relevance ranking. Every hit keeps the
// zero Score, so SearchVisible's stable sort preserves per-KB table order.
func (p *PostgresIndex) searchLike(ctx context.Context, kbID int64, q, like string, limit int) ([]SearchHit, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT document_id, chunk_index, rel_path, byte_offset, content
		 FROM kb_chunks
		 WHERE kb_id = $1 AND content ILIKE $2
		 ORDER BY document_id, chunk_index
		 LIMIT $3`, kbID, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SearchHit
	for rows.Next() {
		var h SearchHit
		var content string
		if err := rows.Scan(&h.DocumentID, &h.ChunkIndex, &h.RelPath, &h.ByteOffset, &content); err != nil {
			return nil, err
		}
		h.KBID = kbID
		h.Snippet = makeSnippet(content, q)
		out = append(out, h)
	}
	return out, rows.Err()
}

// Close is a no-op: the caller owns the shared *sql.DB.
func (p *PostgresIndex) Close() error { return nil }

var _ KBIndex = (*PostgresIndex)(nil)
