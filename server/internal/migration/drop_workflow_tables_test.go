package migration

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	_ "modernc.org/sqlite"
)

// legacySchemaForWorkflowDrop simulates a pre-decommission SQLite database that
// still has the workflow / template-run tables AND the indexes that reference
// the FK columns the migration must drop. The indexes are the crux: SQLite
// refuses ALTER TABLE DROP COLUMN while an index still references the column
// ("error in index ... after drop column: no such column"), which is exactly
// the production crash this test guards against.
const legacySchemaForWorkflowDrop = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    key        TEXT PRIMARY KEY,
    applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS issues (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    title               TEXT NOT NULL DEFAULT '',
    default_template_id INTEGER
);

CREATE TABLE IF NOT EXISTS project_templates (
    id   INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS project_template_columns (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    template_id INTEGER NOT NULL REFERENCES project_templates(id)
);

CREATE TABLE IF NOT EXISTS harness_runs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    template_id  INTEGER REFERENCES project_templates(id)
);

CREATE TABLE IF NOT EXISTS gate_jobs (
    id     INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER NOT NULL REFERENCES harness_runs(id)
);

CREATE TABLE IF NOT EXISTS workspace_agents (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    parent_agent_id INTEGER REFERENCES workspace_agents(id)
);

CREATE TABLE IF NOT EXISTS workspace_agent_repos (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_agent_id INTEGER NOT NULL REFERENCES workspace_agents(id)
);

CREATE TABLE IF NOT EXISTS project_preset_imports (
    id INTEGER PRIMARY KEY AUTOINCREMENT
);

-- FK-bearing columns the migration drops + re-adds as plain INTEGER.
CREATE TABLE IF NOT EXISTS agent_messages (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    harness_run_id INTEGER REFERENCES harness_runs(id)
);

CREATE TABLE IF NOT EXISTS workspace_costs (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    harness_run_id INTEGER REFERENCES harness_runs(id)
);

CREATE TABLE IF NOT EXISTS workspace_tasks (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    harness_run_id INTEGER REFERENCES harness_runs(id)
);

CREATE TABLE IF NOT EXISTS blackboard_entries (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    harness_run_id INTEGER REFERENCES harness_runs(id)
);

CREATE TABLE IF NOT EXISTS harness_checks (
    id     INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER REFERENCES harness_runs(id)
);

-- The two indexes that block DROP COLUMN on SQLite. These mirror the real
-- indexes created in store/migrate.go (idx_agent_messages_harness_run) and
-- store/schema.sql (idx_harness_checks_run).
CREATE INDEX IF NOT EXISTS idx_agent_messages_harness_run ON agent_messages(harness_run_id);
CREATE INDEX IF NOT EXISTS idx_harness_checks_run ON harness_checks(run_id);
`

func openLegacyDBForWorkflowDrop(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	store.Driver = "sqlite"
	if _, err := db.Exec(legacySchemaForWorkflowDrop); err != nil {
		t.Fatalf("apply legacy schema: %v", err)
	}
	return db
}

func indexExistsWF(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var got string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&got)
	return err == nil && got == name
}

// TestDropWorkflowTables_SQLite reproduces the production crash: an existing DB
// with idx_agent_messages_harness_run / idx_harness_checks_run present causes
// DROP COLUMN to fail. The migration must drop the dependent indexes first.
func TestDropWorkflowTables_SQLite(t *testing.T) {
	db := openLegacyDBForWorkflowDrop(t)
	defer db.Close()

	// Seed a row so DROP+ADD column path is exercised against real data.
	if _, err := db.Exec(`INSERT INTO harness_runs (id) VALUES (1)`); err != nil {
		t.Fatalf("seed harness_run: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agent_messages (id, harness_run_id) VALUES (1, 1)`); err != nil {
		t.Fatalf("seed agent_message: %v", err)
	}

	ctx := context.Background()
	if err := MigrateDropWorkflowTables(ctx, db); err != nil {
		t.Fatalf("MigrateDropWorkflowTables: %v", err)
	}

	// Workflow tables must be gone.
	for _, tbl := range dropOrderWorkflow {
		if tableExistsPhase7(db, tbl) {
			t.Errorf("table %q still exists after migration", tbl)
		}
	}

	// issues.default_template_id must be gone.
	if columnExistsPhase7(db, "issues", "default_template_id") {
		t.Errorf("issues.default_template_id still exists after migration")
	}

	// FK columns must be KEPT (as plain INTEGER) for back-compat.
	for _, tc := range []struct{ table, col string }{
		{"agent_messages", "harness_run_id"},
		{"workspace_costs", "harness_run_id"},
		{"workspace_tasks", "harness_run_id"},
		{"blackboard_entries", "harness_run_id"},
		{"harness_checks", "run_id"},
	} {
		if !columnExistsPhase7(db, tc.table, tc.col) {
			t.Errorf("%s.%s should be kept after migration", tc.table, tc.col)
		}
	}

	// The blocking indexes must be gone (they referenced the dropped columns).
	if indexExistsWF(t, db, "idx_agent_messages_harness_run") {
		t.Errorf("idx_agent_messages_harness_run should be dropped before DROP COLUMN")
	}
	if indexExistsWF(t, db, "idx_harness_checks_run") {
		t.Errorf("idx_harness_checks_run should be dropped before DROP COLUMN")
	}

	// Marker set.
	var key string
	if err := db.QueryRow(`SELECT key FROM schema_migrations WHERE key='drop_workflow_tables_v1'`).Scan(&key); err != nil {
		t.Errorf("migration marker not set: %v", err)
	}
}

// TestDropWorkflowTables_Idempotent verifies a second run is a no-op.
func TestDropWorkflowTables_Idempotent(t *testing.T) {
	db := openLegacyDBForWorkflowDrop(t)
	defer db.Close()

	ctx := context.Background()
	if err := MigrateDropWorkflowTables(ctx, db); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := MigrateDropWorkflowTables(ctx, db); err != nil {
		t.Fatalf("second run (idempotent): %v", err)
	}
}

// TestDropWorkflowTables_CurrentSchema proves a fresh install on the latest
// schema (where the workflow tables/indexes never existed) upgrades cleanly.
// Combined with the legacy-schema test, this covers the "any prior version ->
// latest" upgrade requirement: every DROP is existence-guarded.
func TestDropWorkflowTables_CurrentSchema(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store.Driver = "sqlite"
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatalf("apply current schema: %v", err)
	}

	ctx := context.Background()
	if err := MigrateDropWorkflowTables(ctx, db); err != nil {
		t.Fatalf("migrate on current schema: %v", err)
	}
	// Second run is still a no-op.
	if err := MigrateDropWorkflowTables(ctx, db); err != nil {
		t.Fatalf("migrate on current schema (idempotent): %v", err)
	}
	// FK columns still present (kept for back-compat).
	if !columnExistsPhase7(db, "agent_messages", "harness_run_id") {
		t.Errorf("agent_messages.harness_run_id should remain on current schema")
	}
}
