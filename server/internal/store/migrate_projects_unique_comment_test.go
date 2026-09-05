package store

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStripSQLComments(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"line comment dropped", "a\n-- UNIQUE mention\nb\n", "a\n\nb\n"},
		{"block comment dropped", "a /* UNIQUE */ b", "a  b"},
		{"code kept", "UNIQUE(name)", "UNIQUE(name)"},
		{"unterminated line comment", "a\n-- trailing", "a\n"},
		{"unterminated block comment", "a /* trailing", "a "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, stripSQLComments(c.in))
		})
	}
}

// dropProjectsNameUniqueSQLite decides whether to REBUILD the projects table by
// substring-matching the DDL stored in sqlite_master — which preserves comments
// verbatim. A column comment that merely contains the word must not trigger the
// rebuild: that rebuild recreates projects with only its original columns and
// silently drops every column added since (color, memory_sweep_cron,
// floor_command, ...), which then surfaces far away as "no such column".
func TestDropProjectsNameUnique_IgnoresTheWordInComments(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	// A clean projects table (no real constraint) whose comment mentions the word.
	_, err = db.Exec(`CREATE TABLE projects (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		name        TEXT NOT NULL,
		-- keyed by UNIQUE(category, name) elsewhere; this is only prose
		color       TEXT DEFAULT NULL,
		floor_command TEXT NOT NULL DEFAULT ''
	)`)
	require.NoError(t, err)

	dropProjectsNameUniqueSQLite(db)

	cols := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM pragma_table_info('projects')`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		cols[n] = true
	}
	require.True(t, cols["color"], "comment-only match must not rebuild and drop color")
	require.True(t, cols["floor_command"], "comment-only match must not rebuild and drop floor_command")
}

// The real constraint must still be removed when it is genuinely present.
func TestDropProjectsNameUnique_RemovesRealConstraint(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(`CREATE TABLE projects (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		name        TEXT NOT NULL UNIQUE,
		description TEXT DEFAULT '',
		status      TEXT NOT NULL DEFAULT 'active',
		owner_type  TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
		owner_id    INTEGER NOT NULL DEFAULT 0,
		created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO projects (name) VALUES ('p1')`)
	require.NoError(t, err)

	dropProjectsNameUniqueSQLite(db)

	var ddl string
	require.NoError(t, db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='projects'`).Scan(&ddl))
	require.NotContains(t, stripSQLComments(ddl), "UNIQUE",
		"the table-level global name constraint should be gone")
	require.False(t, strings.Contains(ddl, "projects_new"), "table should be renamed, not left as _new")

	// The pre-existing row survived the rebuild.
	var n int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM projects WHERE name='p1'`).Scan(&n))
	require.Equal(t, 1, n)

	// A same-name project under a DIFFERENT owner is now allowed — that is the point
	// of dropping the global constraint. (Same-owner duplicates stay blocked by the
	// separate per-owner unique index that migrateProjectsOwnerScopedName adds.)
	_, err = db.Exec(`INSERT INTO projects (name, owner_type, owner_id) VALUES ('p1', 'user', 7)`)
	require.NoError(t, err)
}
