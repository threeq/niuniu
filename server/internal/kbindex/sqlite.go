package kbindex

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

// trigramMinChars is the minimum query length the FTS5 trigram tokenizer can
// MATCH. Shorter queries (common for 1-2 character Chinese terms) fall back to
// a LIKE substring scan, which the trigram table does not accelerate but which
// still returns correct results.
const trigramMinChars = 3

// SQLiteIndex is the per-owner SQLite full-text index sidecar. It owns a
// dedicated *sql.DB (its own connection pool, WAL and busy-timeout) that is
// completely independent of the main store, so KB indexing never contends with
// the main database's single-writer lock or with the memory subsystem.
type SQLiteIndex struct {
	db   *sql.DB
	path string
}

// OpenSQLiteIndex opens (creating if needed) the sidecar index database at path
// and ensures its schema. path may be ":memory:" for tests. Parent directories
// are created for file-backed paths.
func OpenSQLiteIndex(path string) (*SQLiteIndex, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("kbindex: create dir: %w", err)
		}
	}
	dsn := path
	if path != ":memory:" {
		dsn = path +
			"?_pragma=journal_mode(WAL)" +
			"&_pragma=busy_timeout(5000)" +
			"&_pragma=synchronous(NORMAL)" +
			"&_pragma=foreign_keys(1)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("kbindex: open: %w", err)
	}
	idx := &SQLiteIndex{db: db, path: path}
	if err := idx.ensureSchema(); err != nil {
		db.Close()
		return nil, err
	}
	return idx, nil
}

func (s *SQLiteIndex) ensureSchema() error {
	stmts := []string{
		// Single FTS5 table: indexed `content` plus UNINDEXED pointer columns.
		// trigram tokenizer gives substring (incl. CJK) matching with no
		// external dependency.
		`CREATE VIRTUAL TABLE IF NOT EXISTS kb_chunks USING fts5(
			kb_id UNINDEXED,
			document_id UNINDEXED,
			chunk_index UNINDEXED,
			rel_path UNINDEXED,
			byte_offset UNINDEXED,
			content,
			tokenize='trigram'
		)`,
		`CREATE TABLE IF NOT EXISTS kb_index_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("kbindex: ensure schema: %w", err)
		}
	}
	return nil
}

// IndexDocument replaces all chunks for (KBID, DocumentID) in one transaction.
func (s *SQLiteIndex) IndexDocument(ctx context.Context, doc IndexDoc) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM kb_chunks WHERE kb_id = ? AND document_id = ?`,
		doc.KBID, doc.DocumentID); err != nil {
		return err
	}
	for _, c := range doc.Chunks {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO kb_chunks(kb_id, document_id, chunk_index, rel_path, byte_offset, content)
			 VALUES(?, ?, ?, ?, ?, ?)`,
			doc.KBID, doc.DocumentID, c.Index, doc.RelPath, c.ByteOffset, c.Content); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteDocument removes all chunks for one document.
func (s *SQLiteIndex) DeleteDocument(ctx context.Context, kbID, documentID int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM kb_chunks WHERE kb_id = ? AND document_id = ?`, kbID, documentID)
	return err
}

// DeleteKB removes all chunks for an entire knowledge base.
func (s *SQLiteIndex) DeleteKB(ctx context.Context, kbID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM kb_chunks WHERE kb_id = ?`, kbID)
	return err
}

// Search returns up to limit ranked hits within one KB. Queries of >= 3 runes
// use the FTS5 trigram index with bm25 ranking; shorter queries fall back to a
// LIKE substring scan so 1-2 character (e.g. Chinese) terms still match.
func (s *SQLiteIndex) Search(ctx context.Context, kbID int64, query string, limit int) ([]SearchHit, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	if utf8.RuneCountInString(q) >= trigramMinChars {
		return s.searchFTS(ctx, kbID, q, limit)
	}
	return s.searchLike(ctx, kbID, q, limit)
}

func (s *SQLiteIndex) searchFTS(ctx context.Context, kbID int64, q string, limit int) ([]SearchHit, error) {
	// Treat the whole query as a literal phrase: wrap in double quotes and
	// double any embedded quotes so FTS5 operators (AND/OR/NEAR/*) in user text
	// are not interpreted.
	phrase := `"` + strings.ReplaceAll(q, `"`, `""`) + `"`
	rows, err := s.db.QueryContext(ctx,
		`SELECT document_id, chunk_index, rel_path, byte_offset,
		        snippet(kb_chunks, 5, '[', ']', ' ... ', 12), bm25(kb_chunks)
		 FROM kb_chunks
		 WHERE kb_id = ? AND content MATCH ?
		 ORDER BY bm25(kb_chunks)
		 LIMIT ?`, kbID, phrase, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanHits(rows, kbID)
}

func (s *SQLiteIndex) searchLike(ctx context.Context, kbID int64, q string, limit int) ([]SearchHit, error) {
	// Escape LIKE metacharacters so a literal % or _ in the query matches itself.
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
	rows, err := s.db.QueryContext(ctx,
		`SELECT document_id, chunk_index, rel_path, byte_offset, content, 0.0
		 FROM kb_chunks
		 WHERE kb_id = ? AND content LIKE ? ESCAPE '\'
		 LIMIT ?`, kbID, "%"+esc+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hits, err := scanHits(rows, kbID)
	if err != nil {
		return nil, err
	}
	// Build a snippet around the first match for fallback hits.
	for i := range hits {
		hits[i].Snippet = makeSnippet(hits[i].Snippet, q)
	}
	return hits, nil
}

func scanHits(rows *sql.Rows, kbID int64) ([]SearchHit, error) {
	var out []SearchHit
	for rows.Next() {
		var h SearchHit
		var text string
		var score float64
		if err := rows.Scan(&h.DocumentID, &h.ChunkIndex, &h.RelPath, &h.ByteOffset, &text, &score); err != nil {
			return nil, err
		}
		h.KBID = kbID
		h.Snippet = text // FTS path: already a snippet; LIKE path: raw content, refined by caller
		h.Score = score
		out = append(out, h)
	}
	return out, rows.Err()
}

// makeSnippet returns a short window of content centered on the first
// case-insensitive occurrence of term, bracketing the matched span.
func makeSnippet(content, term string) string {
	const window = 40
	lc := strings.ToLower(content)
	pos := strings.Index(lc, strings.ToLower(term))
	// pos is a byte offset into lc; ToLower can change byte length for some
	// Unicode (e.g. 'İ'), so guard pos > len(content) before slicing the original
	// to avoid a slice-bounds panic. Treat an unreliable offset as "no match".
	if pos < 0 || pos > len(content) {
		if utf8.RuneCountInString(content) <= window {
			return content
		}
		return string([]rune(content)[:window]) + " ..."
	}
	runes := []rune(content)
	// Convert byte position to rune index.
	start := utf8.RuneCountInString(content[:pos])
	end := start + utf8.RuneCountInString(term)
	lo := max(start-window/2, 0)
	hi := min(end+window/2, len(runes))
	var b strings.Builder
	if lo > 0 {
		b.WriteString("... ")
	}
	b.WriteString(string(runes[lo:start]))
	b.WriteString("[")
	b.WriteString(string(runes[start:end]))
	b.WriteString("]")
	b.WriteString(string(runes[end:hi]))
	if hi < len(runes) {
		b.WriteString(" ...")
	}
	return b.String()
}

// Close releases the underlying database handle.
func (s *SQLiteIndex) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Path returns the on-disk location of the sidecar (":memory:" for in-memory).
func (s *SQLiteIndex) Path() string { return s.path }

var _ KBIndex = (*SQLiteIndex)(nil)
