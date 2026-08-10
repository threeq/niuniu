package migration

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/pgtest"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// legacySchemaForWorkflowDropPG mirrors legacySchemaForWorkflowDrop but in
// Postgres dialect, including the FK constraints (named to match the ones the
// migration drops) and the indexes on the FK columns. This proves the PG
// upgrade path tolerates indexed FK columns: unlike SQLite, PG auto-drops
// dependent indexes on DROP COLUMN and dependent FKs on DROP TABLE CASCADE,
// so the migration must succeed without the SQLite index-clearing dance.
const legacySchemaForWorkflowDropPG = `
CREATE TABLE schema_migrations (
    key        TEXT PRIMARY KEY,
    applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE project_templates (
    id   BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL DEFAULT ''
);

CREATE TABLE project_template_columns (
    id          BIGSERIAL PRIMARY KEY,
    template_id BIGINT NOT NULL REFERENCES project_templates(id)
);

CREATE TABLE harness_runs (
    id          BIGSERIAL PRIMARY KEY,
    template_id BIGINT REFERENCES project_templates(id)
);

CREATE TABLE gate_jobs (
    id     BIGSERIAL PRIMARY KEY,
    run_id BIGINT NOT NULL REFERENCES harness_runs(id)
);

CREATE TABLE workspace_agents (
    id              BIGSERIAL PRIMARY KEY,
    parent_agent_id BIGINT REFERENCES workspace_agents(id)
);

CREATE TABLE workspace_agent_repos (
    id                 BIGSERIAL PRIMARY KEY,
    workspace_agent_id BIGINT NOT NULL REFERENCES workspace_agents(id)
);

CREATE TABLE project_preset_imports (
    id BIGSERIAL PRIMARY KEY
);

CREATE TABLE issues (
    id                  BIGSERIAL PRIMARY KEY,
    title               TEXT NOT NULL DEFAULT '',
    default_template_id BIGINT,
    CONSTRAINT fk_issues_default_template_id FOREIGN KEY (default_template_id) REFERENCES project_templates(id)
);
CREATE INDEX idx_issues_default_template ON issues(default_template_id);

CREATE TABLE agent_messages (
    id             BIGSERIAL PRIMARY KEY,
    harness_run_id BIGINT,
    CONSTRAINT fk_agent_messages_harness_run_id FOREIGN KEY (harness_run_id) REFERENCES harness_runs(id)
);
CREATE INDEX idx_agent_messages_harness_run ON agent_messages(harness_run_id);

CREATE TABLE workspace_costs (
    id             BIGSERIAL PRIMARY KEY,
    harness_run_id BIGINT,
    CONSTRAINT fk_workspace_costs_harness_run_id FOREIGN KEY (harness_run_id) REFERENCES harness_runs(id)
);

CREATE TABLE workspace_tasks (
    id             BIGSERIAL PRIMARY KEY,
    harness_run_id BIGINT,
    CONSTRAINT fk_workspace_tasks_harness_run_id FOREIGN KEY (harness_run_id) REFERENCES harness_runs(id)
);

CREATE TABLE blackboard_entries (
    id             BIGSERIAL PRIMARY KEY,
    harness_run_id BIGINT REFERENCES harness_runs(id)
);

CREATE TABLE harness_checks (
    id     BIGSERIAL PRIMARY KEY,
    run_id BIGINT REFERENCES harness_runs(id)
);
CREATE INDEX idx_harness_checks_run ON harness_checks(run_id);
`

// TestDropWorkflowTables_Postgres exercises the PG upgrade path against a
// legacy schema. Auto-skips when Docker / NIUNIU_TEST_PG_DSN is unavailable.
func TestDropWorkflowTables_Postgres(t *testing.T) {
	dsn := pgtest.NewSchemaDSN(t) // skips if no container/DSN
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	defer db.Close()

	prev := store.Driver
	t.Cleanup(func() { store.Driver = prev })
	store.Driver = "postgres"

	if _, err := db.Exec(legacySchemaForWorkflowDropPG); err != nil {
		t.Fatalf("apply legacy PG schema: %v", err)
	}
	// Seed an indexed FK value so DROP COLUMN must clear a non-empty index.
	if _, err := db.Exec(`INSERT INTO harness_runs DEFAULT VALUES`); err != nil {
		t.Fatalf("seed harness_run: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agent_messages (harness_run_id) VALUES (1)`); err != nil {
		t.Fatalf("seed agent_message: %v", err)
	}

	ctx := context.Background()
	if err := MigrateDropWorkflowTables(ctx, db); err != nil {
		t.Fatalf("MigrateDropWorkflowTables (PG): %v", err)
	}
	// Idempotent second run.
	if err := MigrateDropWorkflowTables(ctx, db); err != nil {
		t.Fatalf("MigrateDropWorkflowTables (PG) second run: %v", err)
	}

	// Workflow tables gone.
	for _, tbl := range dropOrderWorkflow {
		var n int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM information_schema.tables WHERE table_name=$1`, tbl,
		).Scan(&n); err != nil {
			t.Fatalf("check table %s: %v", tbl, err)
		}
		if n != 0 {
			t.Errorf("table %q still exists after PG migration", tbl)
		}
	}

	// FK columns kept; default_template_id and its index gone.
	cols := map[string]string{
		"agent_messages":     "harness_run_id",
		"workspace_costs":    "harness_run_id",
		"workspace_tasks":    "harness_run_id",
		"blackboard_entries": "harness_run_id",
		"harness_checks":     "run_id",
	}
	for tbl, col := range cols {
		var n int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM information_schema.columns WHERE table_name=$1 AND column_name=$2`,
			tbl, col,
		).Scan(&n); err != nil {
			t.Fatalf("check column %s.%s: %v", tbl, col, err)
		}
		if n == 0 {
			t.Errorf("%s.%s should be kept after PG migration", tbl, col)
		}
	}
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM information_schema.columns WHERE table_name='issues' AND column_name='default_template_id'`,
	).Scan(&n); err != nil {
		t.Fatalf("check issues.default_template_id: %v", err)
	}
	if n != 0 {
		t.Errorf("issues.default_template_id should be dropped after PG migration")
	}
}
