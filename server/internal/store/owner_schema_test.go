package store

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func openMem(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(Schema); err != nil {
		t.Fatal(err)
	}
	return db
}

func tableExistsForTest(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var got string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&got)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return got == name
}

func TestSchemaHasNewOwnerTables(t *testing.T) {
	db := openMem(t)
	defer db.Close()
	for _, tbl := range []string{"organizations", "org_members", "org_audit_log", "mcp_session_tokens"} {
		if !tableExistsForTest(t, db, tbl) {
			t.Errorf("table %q not created by schema.sql", tbl)
		}
	}
}

// Renamed to avoid clash with the package's own columnExists (open.go:24).
func columnExistsForTest(t *testing.T, db *sql.DB, table, col string) bool {
	t.Helper()
	rows, err := db.Query("SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var got string
		if err := rows.Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got == col {
			return true
		}
	}
	return false
}

func TestOwnerColumnsOnTopLevelTables(t *testing.T) {
	db := openMem(t)
	defer db.Close()
	Migrate(db)

	// Note: 'harnesses' and 'teams' were removed in Phase 7. 'harness_specs' is
	// no longer owner-scoped (it is a single global library).
	tables := []string{
		"projects", "repositories", "workspaces",
		"env_presets", "quick_actions",
		"agents",
	}
	for _, tbl := range tables {
		for _, col := range []string{"owner_type", "owner_id"} {
			if !columnExistsForTest(t, db, tbl, col) {
				t.Errorf("table %q missing column %q after migration", tbl, col)
			}
		}
	}
}

func indexExistsForTest(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var got string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&got)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return got == name
}

func TestUniqueConstraintMigration(t *testing.T) {
	db := openMem(t)
	defer db.Close()
	Migrate(db)

	// Note: idx_teams_owner_name_unique and idx_harnesses_owner_name_unique are
	// omitted — those tables were dropped in Phase 7 (drop_legacy_phase7_v1).
	for _, idx := range []string{
		"idx_projects_owner_name_unique",
		"idx_repositories_owner_remote_unique",
		"idx_quick_actions_owner_label_unique",
		"idx_env_presets_owner_name_unique",
		"idx_agents_owner_name_unique",
	} {
		if !indexExistsForTest(t, db, idx) {
			t.Errorf("expected UNIQUE index %q after migration", idx)
		}
	}
}

func TestUniqueConstraintEnforcesInsertion(t *testing.T) {
	db := openMem(t)
	defer db.Close()
	Migrate(db)

	mustInsert := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("expected insert to succeed: %v", err)
		}
	}
	mustFail := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err == nil {
			t.Fatalf("expected insert to fail with UNIQUE violation, but it succeeded")
		}
	}

	// env_presets: schema.sql has a column-level UNIQUE on name, so cross-owner
	// same-name inserts are blocked by the legacy constraint (C1 limitation —
	// deferred to a follow-up migration). We test within-owner uniqueness only:
	// same owner + same name must fail (the per-owner index catches this too).
	mustInsert(`INSERT INTO env_presets(name, owner_type, owner_id) VALUES(?, 'user', 1)`, "env-a")
	mustInsert(`INSERT INTO env_presets(name, owner_type, owner_id) VALUES(?, 'user', 2)`, "env-b")
	// Duplicate: same owner_type+owner_id+name — must fail.
	mustFail(`INSERT INTO env_presets(name, owner_type, owner_id) VALUES(?, 'user', 1)`, "env-a")

	// quick_actions: no legacy column-level UNIQUE on label, so cross-owner
	// same-label succeeds, and only duplicate same-owner rows are rejected.
	mustInsert(`INSERT INTO quick_actions(label, content, owner_type, owner_id) VALUES('label-a', '', 'user', 1)`)
	mustInsert(`INSERT INTO quick_actions(label, content, owner_type, owner_id) VALUES('label-a', '', 'user', 2)`)
	// Duplicate: same owner_type+owner_id+label — must fail.
	mustFail(`INSERT INTO quick_actions(label, content, owner_type, owner_id) VALUES('label-a', '', 'user', 1)`)

	// repositories: partial index excludes empty git_remote, so two local-only
	// repos under the same owner may both have git_remote=''. Non-empty remotes
	// must be unique per owner.
	mustInsert(`INSERT INTO repositories(name, path, git_remote, owner_type, owner_id) VALUES('r1', '/tmp/a', '', 'user', 1)`)
	// Second local-only repo for the same owner — must succeed (partial index skips empty).
	mustInsert(`INSERT INTO repositories(name, path, git_remote, owner_type, owner_id) VALUES('r2', '/tmp/b', '', 'user', 1)`)
	// First repo with a non-empty remote — must succeed.
	mustInsert(`INSERT INTO repositories(name, path, git_remote, owner_type, owner_id) VALUES('r3', '/tmp/c', 'git@x:r', 'user', 1)`)
	// Duplicate non-empty remote for same owner — must fail.
	mustFail(`INSERT INTO repositories(name, path, git_remote, owner_type, owner_id) VALUES('r4', '/tmp/d', 'git@x:r', 'user', 1)`)
}
