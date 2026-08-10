package store

import (
	"context"
	"database/sql"
	"strings"
)

// DB is a driver-aware wrapper around *sql.DB. ExecContext, QueryContext,
// QueryRowContext, and PrepareContext route through ConvertPlaceholders so
// SQLite-style "?" placeholders become "$1, $2, ..." on Postgres while
// passing through unchanged on SQLite. BeginTx returns a *Tx with the same
// rewriting behaviour, plus a Queries method that yields a sqlc *Queries
// already bound to the tx with the right (PgTx / raw) wrapping for the
// active driver.
//
// Service code should hold *DB rather than *sql.DB so that the type system
// — not a hand-written pgQ() call — is what guarantees a placeholder
// rewrite happens. The previous arrangement (services held *sql.DB and
// individual call sites had to remember to call pgQ) is what made
// "syntax error at end of input" leak into production twice in a row.
type DB struct {
	raw *sql.DB
}

// Wrap returns a *DB around the given *sql.DB. Idempotency is preserved by
// keeping the wrapper stateless — multiple wraps over the same *sql.DB are
// safe and equivalent.
//
// Wrap(nil) returns nil so test fixtures that previously passed a bare
// `nil` *sql.DB continue to work — service code that nil-checks `s.db`
// keeps the same observable behaviour after the wrapper rolled out.
func Wrap(db *sql.DB) *DB {
	if db == nil {
		return nil
	}
	return &DB{raw: db}
}

// Raw exposes the underlying *sql.DB for the rare API that requires it
// (e.g. http.Hijacker style libraries that own the DB lifecycle, or tests).
// Prefer the wrapper's methods over Raw — using Raw bypasses placeholder
// rewriting and is the easiest way to reintroduce the bug class this type
// was created to prevent.
func (w *DB) Raw() *sql.DB { return w.raw }

// Close, Ping, PingContext, Stats, SetMaxOpenConns are pass-throughs so a
// *DB can substitute for a *sql.DB at the lifecycle-management edges.
func (w *DB) Close() error                          { return w.raw.Close() }
func (w *DB) Ping() error                           { return w.raw.Ping() }
func (w *DB) PingContext(ctx context.Context) error { return w.raw.PingContext(ctx) }
func (w *DB) Stats() sql.DBStats                    { return w.raw.Stats() }
func (w *DB) SetMaxOpenConns(n int)                 { w.raw.SetMaxOpenConns(n) }
func (w *DB) SetMaxIdleConns(n int)                 { w.raw.SetMaxIdleConns(n) }

func (w *DB) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return w.raw.ExecContext(ctx, port(q), args...)
}

func (w *DB) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return w.raw.QueryContext(ctx, port(q), args...)
}

func (w *DB) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return w.raw.QueryRowContext(ctx, port(q), args...)
}

func (w *DB) PrepareContext(ctx context.Context, q string) (*sql.Stmt, error) {
	return w.raw.PrepareContext(ctx, port(q))
}

// BeginTx starts a tx and returns it wrapped. Defer the *Tx, not the
// underlying *sql.Tx — *Tx.Rollback handles already-committed cases the
// same way *sql.Tx does.
func (w *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*Tx, error) {
	rawTx, err := w.raw.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &Tx{raw: rawTx}, nil
}

// Queries returns a sqlc *Queries bound to this *DB. On Postgres it routes
// generated queries through PgDB so "?" placeholders become "$N"; on SQLite
// it binds the raw *sql.DB directly.
func (w *DB) Queries() *Queries {
	if Driver == "postgres" {
		return New(&PgDB{DB: w.raw})
	}
	return New(w.raw)
}

// Tx wraps *sql.Tx with the same placeholder rewriting as *DB.
type Tx struct {
	raw *sql.Tx
}

// Raw exposes the underlying *sql.Tx. Same caveats as DB.Raw.
func (t *Tx) Raw() *sql.Tx { return t.raw }

func (t *Tx) Commit() error   { return t.raw.Commit() }
func (t *Tx) Rollback() error { return t.raw.Rollback() }

func (t *Tx) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return t.raw.ExecContext(ctx, port(q), args...)
}

func (t *Tx) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return t.raw.QueryContext(ctx, port(q), args...)
}

func (t *Tx) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return t.raw.QueryRowContext(ctx, port(q), args...)
}

func (t *Tx) PrepareContext(ctx context.Context, q string) (*sql.Stmt, error) {
	return t.raw.PrepareContext(ctx, port(q))
}

// Queries returns a sqlc *Queries bound to this *Tx. Driver-aware: on
// Postgres the underlying *sql.Tx is wrapped with PgTx so generated "?"
// placeholders become "$N" automatically. Replaces the old
// `s.q.WithTx(tx)` pattern, which silently bound the raw tx and shipped
// "?" straight to pgx.
func (t *Tx) Queries() *Queries {
	return NewQueriesTx(t.raw)
}

// port rewrites SQLite-flavored SQL into the dialect of the active driver.
// Identity on SQLite. On Postgres:
//   - "INSERT OR IGNORE INTO foo ..." → "INSERT INTO foo ... ON CONFLICT DO NOTHING"
//     (relies on the table having a unique constraint that triggers the conflict;
//     org_members PK (org_id,user_id) and schema_migrations PK (key) both qualify).
//   - "?" placeholders → "$1, $2, ..." via ConvertPlaceholders.
//
// Idempotent: calling on a query already in $N form is a no-op
// (ConvertPlaceholders short-circuits when no "?" is present).
func port(query string) string {
	if Driver != "postgres" {
		return query
	}
	if hasInsertOrIgnorePrefix(query) {
		query = strings.Replace(query, "INSERT OR IGNORE", "INSERT", 1) + " ON CONFLICT DO NOTHING"
	}
	return ConvertPlaceholders(query)
}

// hasInsertOrIgnorePrefix returns true if, after skipping any leading
// whitespace and SQL line comments, the query begins with "INSERT OR IGNORE".
// sqlc-generated query strings carry a leading `-- name: ... :exec\n`
// preamble, so a plain HasPrefix on the original input misses them and lets
// the SQLite-only syntax leak through to PostgreSQL ("syntax error at or
// near OR" with SQLSTATE 42601). Handles arbitrarily many leading -- lines
// and surrounding whitespace.
func hasInsertOrIgnorePrefix(query string) bool {
	s := query
	for {
		s = strings.TrimLeft(s, " \t\r\n")
		if !strings.HasPrefix(s, "--") {
			break
		}
		nl := strings.IndexByte(s, '\n')
		if nl < 0 {
			return false // comment runs to EOF; no statement after it
		}
		s = s[nl+1:]
	}
	return strings.HasPrefix(s, "INSERT OR IGNORE")
}
