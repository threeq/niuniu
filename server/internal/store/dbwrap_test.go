package store

import "testing"

// TestPortRewrite covers the unified driver-aware rewriter that *DB and *Tx
// route every query through. It is exercised indirectly by every service test
// hitting an in-memory sqlite, but explicit cases here document the contract:
//   - SQLite: identity, no surprises.
//   - Postgres: "?" → "$N" (single-quoted literals untouched), and
//     "INSERT OR IGNORE INTO foo ..." → "INSERT INTO foo ... ON CONFLICT DO NOTHING".
//
// The previous version of this test lived in internal/migration and tested a
// duplicate portQuery helper; that helper was removed when MigrateOwnerModel
// switched to *store.DB.
func TestPortRewrite(t *testing.T) {
	original := Driver
	defer func() { Driver = original }()

	Driver = "sqlite"
	for _, c := range []struct{ in, want string }{
		{`SELECT id FROM users WHERE username = ?`, `SELECT id FROM users WHERE username = ?`},
		{`INSERT OR IGNORE INTO foo (k) VALUES (?)`, `INSERT OR IGNORE INTO foo (k) VALUES (?)`},
		{`UPDATE x SET a = ?, b = ? WHERE c = ?`, `UPDATE x SET a = ?, b = ? WHERE c = ?`},
	} {
		if got := port(c.in); got != c.want {
			t.Errorf("port(sqlite, %q) = %q; want %q", c.in, got, c.want)
		}
	}

	Driver = "postgres"
	for _, c := range []struct{ in, want string }{
		{
			`SELECT id FROM users WHERE username = ?`,
			`SELECT id FROM users WHERE username = $1`,
		},
		{
			`UPDATE x SET a = ?, b = ? WHERE c = ?`,
			`UPDATE x SET a = $1, b = $2 WHERE c = $3`,
		},
		{
			`INSERT OR IGNORE INTO foo (k) VALUES (?)`,
			`INSERT INTO foo (k) VALUES ($1) ON CONFLICT DO NOTHING`,
		},
		{
			`INSERT OR IGNORE INTO org_members (org_id, user_id, role) VALUES (?, ?, ?)`,
			`INSERT INTO org_members (org_id, user_id, role) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		},
		{
			`INSERT OR IGNORE INTO schema_migrations (key) VALUES ('owner_model_v1')`,
			`INSERT INTO schema_migrations (key) VALUES ('owner_model_v1') ON CONFLICT DO NOTHING`,
		},
		{
			// Single-quoted string literal containing a question mark must NOT
			// be converted — relied on by ConvertPlaceholders.
			`SELECT 'a?b' FROM x WHERE id = ?`,
			`SELECT 'a?b' FROM x WHERE id = $1`,
		},
		{
			// sqlc-generated queries carry a leading `-- name: Foo :exec\n`
			// preamble. The rewriter must look past it. Regression test for
			// the bug where AddIssueAssignee / AddIssueLabel leaked
			// `INSERT OR IGNORE` to PG and crashed with SQLSTATE 42601.
			"-- name: AddIssueAssignee :exec\nINSERT OR IGNORE INTO issue_assignees (issue_id, user_id) VALUES (?, ?)\n",
			"-- name: AddIssueAssignee :exec\nINSERT INTO issue_assignees (issue_id, user_id) VALUES ($1, $2)\n ON CONFLICT DO NOTHING",
		},
		{
			// Whitespace before the comment, multiple -- lines.
			"  -- name: Foo :exec\n  -- another\nINSERT OR IGNORE INTO x (a) VALUES (?)",
			"  -- name: Foo :exec\n  -- another\nINSERT INTO x (a) VALUES ($1) ON CONFLICT DO NOTHING",
		},
	} {
		if got := port(c.in); got != c.want {
			t.Errorf("port(postgres, %q) = %q; want %q", c.in, got, c.want)
		}
	}
}
