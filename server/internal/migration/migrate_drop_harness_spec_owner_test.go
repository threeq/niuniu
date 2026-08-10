package migration

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	_ "modernc.org/sqlite"
)

// oldShapeHarnessSpecsDDL is the pre-drop harness_specs table (with the legacy
// scope / project_id / owner_type / owner_id columns) used to exercise the
// upgrade path of MigrateDropHarnessSpecOwner.
const oldShapeHarnessSpecsDDL = `
CREATE TABLE harness_specs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    scope       TEXT NOT NULL DEFAULT 'global',
    project_id  INTEGER,
    category    TEXT NOT NULL,
    name        TEXT NOT NULL,
    enabled     INTEGER NOT NULL DEFAULT 1,
    severity    TEXT NOT NULL DEFAULT 'warning',
    config      TEXT NOT NULL DEFAULT '{}',
    kind                TEXT    NOT NULL DEFAULT 'regex_match',
    target              TEXT    NOT NULL DEFAULT '',
    pattern             TEXT    NOT NULL DEFAULT '',
    pattern_flags       TEXT    NOT NULL DEFAULT '',
    command             TEXT    NOT NULL DEFAULT '',
    timeout_sec         INTEGER NOT NULL DEFAULT 120,
    expected_exit_code  INTEGER NOT NULL DEFAULT 0,
    extract_regex       TEXT    NOT NULL DEFAULT '',
    threshold_value     REAL    NOT NULL DEFAULT 0,
    threshold_op        TEXT    NOT NULL DEFAULT '',
    file_paths          TEXT    NOT NULL DEFAULT '[]',
    trigger_on          TEXT    NOT NULL DEFAULT 'phase_exit',
    judge_prompt        TEXT    NOT NULL DEFAULT '',
    judge_model         TEXT    NOT NULL DEFAULT '',
    owner_type  TEXT NOT NULL DEFAULT 'user',
    owner_id    INTEGER NOT NULL DEFAULT 0,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(scope, project_id, category, name)
);`

// TestMigrateDropHarnessSpecOwner_DropsColumns verifies the upgrade path: an old
// harness_specs (with scope/owner/project_id) is rebuilt into the global shape,
// legacy rows are preserved and deduped by (category, name), and the new
// UNIQUE(category, name) is enforced.
func TestMigrateDropHarnessSpecOwner_DropsColumns(t *testing.T) {
	prev := store.Driver
	t.Cleanup(func() { store.Driver = prev })
	store.Driver = "sqlite"

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(oldShapeHarnessSpecsDDL); err != nil {
		t.Fatalf("create old-shape harness_specs: %v", err)
	}
	// Two rows sharing (category,name) — the loose old unique (project_id NULL)
	// allowed duplicates; the migration must dedup to one.
	if _, err := db.Exec(`INSERT INTO harness_specs (scope, category, name, owner_type, owner_id) VALUES
		('global', 'commit', 'branch-name', 'org', 5),
		('global', 'commit', 'branch-name', 'user', 0)`); err != nil {
		t.Fatalf("seed rows: %v", err)
	}

	if err := MigrateDropHarnessSpecOwner(context.Background(), db); err != nil {
		t.Fatalf("MigrateDropHarnessSpecOwner: %v", err)
	}

	// Legacy columns must be gone.
	for _, col := range []string{"scope", "project_id", "owner_type", "owner_id"} {
		var n int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('harness_specs') WHERE name = ?`, col).Scan(&n); err != nil {
			t.Fatalf("pragma_table_info(%s): %v", col, err)
		}
		if n != 0 {
			t.Errorf("column %q still present after migration", col)
		}
	}

	// Deduped to a single row.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM harness_specs`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("row count = %d; want 1 (deduped)", count)
	}

	// UNIQUE(category, name) is enforced.
	if _, err := db.Exec(`INSERT INTO harness_specs (category, name) VALUES ('commit', 'branch-name')`); err == nil {
		t.Error("expected UNIQUE(category,name) violation on duplicate insert, got nil")
	}

	// Idempotent: a second run is a no-op.
	if err := MigrateDropHarnessSpecOwner(context.Background(), db); err != nil {
		t.Fatalf("second MigrateDropHarnessSpecOwner: %v", err)
	}
}
