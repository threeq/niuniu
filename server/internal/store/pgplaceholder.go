package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

// PgDB wraps a *sql.DB and converts ? placeholders to $1, $2, ... for PostgreSQL.
// This allows sqlc-generated code (which uses ? placeholders for SQLite) to work
// with PostgreSQL without regenerating the queries.
type PgDB struct {
	DB *sql.DB
}

func (p *PgDB) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return p.DB.ExecContext(ctx, port(query), args...)
}

func (p *PgDB) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return p.DB.PrepareContext(ctx, port(query))
}

func (p *PgDB) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return p.DB.QueryContext(ctx, port(query), args...)
}

func (p *PgDB) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return p.DB.QueryRowContext(ctx, port(query), args...)
}

// PgTx wraps a *sql.Tx with placeholder conversion for PostgreSQL.
type PgTx struct {
	Tx *sql.Tx
}

func (p *PgTx) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return p.Tx.ExecContext(ctx, port(query), args...)
}

func (p *PgTx) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return p.Tx.PrepareContext(ctx, port(query))
}

func (p *PgTx) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return p.Tx.QueryContext(ctx, port(query), args...)
}

func (p *PgTx) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return p.Tx.QueryRowContext(ctx, port(query), args...)
}

// NewQueriesTx creates a Queries instance from a *sql.Tx.
// For PostgreSQL, it wraps the tx with placeholder conversion.
func NewQueriesTx(tx *sql.Tx) *Queries {
	if Driver == "postgres" {
		return New(&PgTx{Tx: tx})
	}
	return New(tx)
}

// ConvertPlaceholders replaces ? with $1, $2, ... while skipping
// occurrences inside single-quoted string literals.
//
// sqlc emits two placeholder shapes depending on whether the source SQL used
// `sqlc.arg(name)` (named) or plain `?` (positional):
//   - plain `?` becomes `?` in the generated query and we assign $1, $2, ...
//     by order of appearance
//   - `sqlc.arg(name)` becomes `?N` (e.g. `?8`) where N is the field's
//     position in the params struct
//
// A naive `?` -> `$<counter>` rewrite collides on the second shape: the `?`
// is replaced but the trailing decimal digits survive, producing `$N` followed
// by leftover digits (e.g. `?8` becomes `$N8` where N is the running counter,
// reaching PG as e.g. `$88`). PG then rejects the query because the index
// exceeds the bind set, surfacing as `bind message supplies N parameters,
// but prepared statement requires NN` or SQLSTATE 42P18.
//
// To stay correct across both shapes we look ahead one byte after each `?`.
// If the next byte is a digit we consume the full decimal run and emit
// `$<that-number>`, preserving sqlc's intent. Otherwise we emit the running
// `$<counter>`. The two shapes can coexist in the same query because sqlc's
// numbered form picks the parameter's positional index, which matches what
// our counter would have produced anyway for that slot.
func ConvertPlaceholders(query string) string {
	if !strings.Contains(query, "?") {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 16)
	n := 1
	inString := false
	for i := 0; i < len(query); i++ {
		ch := query[i]
		if ch == '\'' {
			inString = !inString
			b.WriteByte(ch)
			continue
		}
		if ch != '?' || inString {
			b.WriteByte(ch)
			continue
		}
		// Look ahead for sqlc's `?N` numbered form. When present, the
		// decimal run that follows the `?` IS the parameter index and we
		// emit `$<index>` verbatim. We still advance n so a subsequent
		// plain `?` slot does not collide on the same index (sqlc-emitted
		// numbered forms always reference an index <= len(params), and the
		// counter is a safe upper bound for any plain `?` afterwards).
		j := i + 1
		for j < len(query) && query[j] >= '0' && query[j] <= '9' {
			j++
		}
		if j > i+1 {
			b.WriteByte('$')
			b.WriteString(query[i+1 : j])
			i = j - 1 // consumed the digits; outer loop's i++ moves past
			n++
			continue
		}
		b.WriteByte('$')
		b.WriteString(strconv.Itoa(n))
		n++
	}
	return b.String()
}
