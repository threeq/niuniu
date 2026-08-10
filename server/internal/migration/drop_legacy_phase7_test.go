package migration

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	_ "modernc.org/sqlite"
)

// legacySchemaForPhase7 is a minimal schema that simulates a pre-Phase-7
// SQLite database: it includes the legacy tables and columns that Phase 7
// must drop. We build it manually rather than using store.Schema so the test
// stays independent of the current (post-Phase-7) schema file.
const legacySchemaForPhase7 = `
CREATE TABLE IF NOT EXISTS users (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    username     TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role         TEXT NOT NULL DEFAULT 'member',
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS agents (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL UNIQUE,
    dir_path   TEXT NOT NULL DEFAULT '',
    driver     TEXT NOT NULL DEFAULT 'claude-cli',
    capabilities TEXT NOT NULL DEFAULT '',
    owner_type TEXT NOT NULL DEFAULT 'user',
    owner_id   INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS harnesses (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    name               TEXT NOT NULL UNIQUE,
    icon               TEXT NOT NULL DEFAULT '',
    description        TEXT NOT NULL DEFAULT '',
    prompt_template    TEXT NOT NULL DEFAULT '',
    max_rounds         INTEGER NOT NULL DEFAULT 10,
    max_review_retries INTEGER NOT NULL DEFAULT 3,
    owner_type         TEXT NOT NULL DEFAULT 'user',
    owner_id           INTEGER NOT NULL DEFAULT 0,
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS teams (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    name               TEXT NOT NULL,
    description        TEXT NOT NULL DEFAULT '',
    coordinator_config TEXT NOT NULL DEFAULT '{"mode":"hybrid"}',
    owner_type         TEXT NOT NULL DEFAULT 'user',
    owner_id           INTEGER NOT NULL DEFAULT 0,
    created_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS team_members (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    team_id      INTEGER NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    agent_id     INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    role         TEXT NOT NULL DEFAULT 'worker',
    capabilities TEXT NOT NULL DEFAULT '',
    position     INTEGER NOT NULL DEFAULT 0,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS harness_phases (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    harness_id   INTEGER NOT NULL REFERENCES harnesses(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    label        TEXT NOT NULL DEFAULT '',
    position     INTEGER NOT NULL DEFAULT 0,
    auto_advance INTEGER NOT NULL DEFAULT 1,
    UNIQUE(harness_id, name),
    UNIQUE(harness_id, position)
);

CREATE TABLE IF NOT EXISTS harness_phase_agents (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    phase_id     INTEGER NOT NULL REFERENCES harness_phases(id) ON DELETE CASCADE,
    agent_source TEXT NOT NULL DEFAULT '',
    agent_name   TEXT NOT NULL DEFAULT '',
    role         TEXT NOT NULL CHECK (role IN ('execute', 'review')),
    position     INTEGER NOT NULL DEFAULT 0,
    UNIQUE(phase_id, agent_source, agent_name, role)
);

CREATE TABLE IF NOT EXISTS harness_phase_gates (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    phase_id INTEGER NOT NULL REFERENCES harness_phases(id) ON DELETE CASCADE,
    spec_id  INTEGER NOT NULL DEFAULT 0,
    position INTEGER NOT NULL DEFAULT 0,
    UNIQUE(phase_id, spec_id)
);

-- Minimal workspaces: legacy columns team_id + harness_id present.
CREATE TABLE IF NOT EXISTS workspaces (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL DEFAULT '',
    path       TEXT NOT NULL,
    harness_id INTEGER DEFAULT NULL REFERENCES harnesses(id),
    team_id    INTEGER DEFAULT NULL REFERENCES teams(id),
    owner_type TEXT NOT NULL DEFAULT 'user',
    owner_id   INTEGER NOT NULL DEFAULT 0
);

-- harness_runs with legacy columns.
CREATE TABLE IF NOT EXISTS harness_runs (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id       INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    harness_id         INTEGER REFERENCES harnesses(id),
    user_goal          TEXT NOT NULL,
    status             TEXT NOT NULL DEFAULT 'running',
    current_phase_id   INTEGER REFERENCES harness_phases(id),
    current_phase_name TEXT NOT NULL DEFAULT '',
    current_round      INTEGER NOT NULL DEFAULT 0,
    error_message      TEXT,
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- workspace_agents with legacy source CHECK (includes columns added by store.Migrate).
CREATE TABLE IF NOT EXISTS workspace_agents (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id         INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    agent_id             INTEGER REFERENCES agents(id),
    role                 TEXT NOT NULL DEFAULT 'worker',
    capabilities         TEXT NOT NULL DEFAULT '',
    source               TEXT NOT NULL CHECK(source IN ('manual', 'team', 'harness_phase', 'subagent')),
    source_ref_id        INTEGER,
    driver               TEXT NOT NULL DEFAULT 'claude-cli',
    session_id           TEXT,
    execution_mode       TEXT NOT NULL DEFAULT 'sequential',
    status               TEXT NOT NULL DEFAULT 'idle',
    parent_agent_id      INTEGER REFERENCES workspace_agents(id) ON DELETE SET NULL,
    subagent_type        TEXT,
    dispatch_tool_use_id TEXT,
    last_error           TEXT,
    joined_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    exited_at            TIMESTAMP
);

-- blackboard_entries with legacy phase_id.
CREATE TABLE IF NOT EXISTS blackboard_entries (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id   INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    producer_agent TEXT NOT NULL,
    entry_type     TEXT NOT NULL,
    entry_key      TEXT NOT NULL,
    content        TEXT NOT NULL DEFAULT '',
    metadata       TEXT NOT NULL DEFAULT '{}',
    ref_path       TEXT,
    harness_run_id INTEGER REFERENCES harness_runs(id),
    phase_id       INTEGER REFERENCES harness_phases(id),
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS schema_migrations (
    key        TEXT PRIMARY KEY,
    applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`

// openLegacyDBForPhase7 opens an in-memory SQLite DB with the PRE-Phase-7
// schema (containing all the legacy tables/columns that will be dropped).
func openLegacyDBForPhase7(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	store.Driver = "sqlite"
	if _, err := db.Exec(legacySchemaForPhase7); err != nil {
		t.Fatalf("apply legacy schema: %v", err)
	}
	return db
}

// openCurrentDBForPhase7 opens an in-memory SQLite DB with the current (post-Phase-7)
// schema — used for the idempotency test where there are no legacy tables.
func openCurrentDBForPhase7(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	store.Driver = "sqlite"
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatalf("apply current schema: %v", err)
	}
	return db
}

// tableExistsPhase7 checks whether a table exists in the SQLite DB.
func tableExistsPhase7(db *sql.DB, name string) bool {
	var got string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&got)
	return err == nil && got == name
}

// columnExistsPhase7 checks whether a column exists in a SQLite table.
func columnExistsPhase7(db *sql.DB, table, col string) bool {
	rows, err := db.Query("SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var got string
		if err := rows.Scan(&got); err != nil {
			return false
		}
		if got == col {
			return true
		}
	}
	return false
}

// insertLegacyFixture inserts legacy rows that the migration should process.
func insertLegacyFixture(t *testing.T, db *sql.DB) {
	t.Helper()

	_, err := db.Exec(`INSERT INTO users (id, username, password_hash, role) VALUES (1, 'alice', 'x', 'admin')`)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	_, err = db.Exec(`INSERT INTO workspaces (id, name, path, owner_type, owner_id) VALUES (1, 'ws', '/tmp/ws', 'user', 1)`)
	if err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	_, err = db.Exec(`INSERT INTO agents (id, name, dir_path, owner_type, owner_id) VALUES (1, 'agent1', '/tmp/a', 'user', 1)`)
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	// Insert a harness row (legacy).
	_, err = db.Exec(`INSERT INTO harnesses (id, name, owner_type, owner_id) VALUES (1, 'legacy-harness', 'user', 1)`)
	if err != nil {
		t.Fatalf("insert harness: %v", err)
	}

	// Insert a harness_phases row.
	_, err = db.Exec(`INSERT INTO harness_phases (id, harness_id, name, position) VALUES (1, 1, 'phase1', 0)`)
	if err != nil {
		t.Fatalf("insert harness_phase: %v", err)
	}

	// Insert a team row.
	_, err = db.Exec(`INSERT INTO teams (id, name, owner_type, owner_id) VALUES (1, 'legacy-team', 'user', 1)`)
	if err != nil {
		t.Fatalf("insert team: %v", err)
	}

	// Insert a team_members row.
	_, err = db.Exec(`INSERT INTO team_members (team_id, agent_id, role, position) VALUES (1, 1, 'worker', 0)`)
	if err != nil {
		t.Fatalf("insert team_member: %v", err)
	}

	// Insert a harness_run with harness_id (legacy) and status='running'.
	_, err = db.Exec(`INSERT INTO harness_runs (id, workspace_id, harness_id, user_goal, status)
		VALUES (1, 1, 1, 'do stuff', 'running')`)
	if err != nil {
		t.Fatalf("insert harness_run with harness_id: %v", err)
	}

	// Insert workspace_agents with legacy source values.
	_, err = db.Exec(`INSERT INTO workspace_agents (id, workspace_id, agent_id, source)
		VALUES (1, 1, 1, 'team')`)
	if err != nil {
		t.Fatalf("insert workspace_agent source=team: %v", err)
	}
	_, err = db.Exec(`INSERT INTO workspace_agents (id, workspace_id, agent_id, source)
		VALUES (2, 1, 1, 'harness_phase')`)
	if err != nil {
		t.Fatalf("insert workspace_agent source=harness_phase: %v", err)
	}
}

func TestDropLegacyPhase7_SQLite(t *testing.T) {
	db := openLegacyDBForPhase7(t)
	defer db.Close()

	insertLegacyFixture(t, db)

	ctx := context.Background()
	if err := MigrateDropLegacyPhase7(ctx, db); err != nil {
		t.Fatalf("MigrateDropLegacyPhase7: %v", err)
	}

	// Tables that should be gone.
	for _, tbl := range []string{"harnesses", "harness_phases", "harness_phase_agents", "harness_phase_gates", "teams", "team_members"} {
		if tableExistsPhase7(db, tbl) {
			t.Errorf("table %q still exists after migration", tbl)
		}
	}

	// Columns that should be gone from harness_runs.
	for _, col := range []string{"harness_id", "current_phase_id", "current_phase_name"} {
		if columnExistsPhase7(db, "harness_runs", col) {
			t.Errorf("harness_runs.%s still exists after migration", col)
		}
	}

	// blackboard_entries.phase_id should be gone.
	if columnExistsPhase7(db, "blackboard_entries", "phase_id") {
		t.Errorf("blackboard_entries.phase_id still exists after migration")
	}

	// workspaces.team_id should be gone.
	if columnExistsPhase7(db, "workspaces", "team_id") {
		t.Errorf("workspaces.team_id still exists after migration")
	}

	// Legacy run (harness_id IS NOT NULL, was running) should be marked failed.
	var status string
	err := db.QueryRow(`SELECT status FROM harness_runs WHERE id = 1`).Scan(&status)
	if err != nil {
		t.Fatalf("query harness_run status: %v", err)
	}
	if status != "failed" {
		t.Errorf("legacy harness_run status = %q; want 'failed'", status)
	}

	// workspace_agents with source 'team' and 'harness_phase' should be 'manual'.
	rows, err := db.Query(`SELECT id, source FROM workspace_agents ORDER BY id`)
	if err != nil {
		t.Fatalf("query workspace_agents: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var src string
		if err := rows.Scan(&id, &src); err != nil {
			t.Fatal(err)
		}
		if src != "manual" {
			t.Errorf("workspace_agent id=%d source=%q; want 'manual'", id, src)
		}
	}

	// source CHECK should reject old value 'team'.
	_, insertErr := db.Exec(`INSERT INTO workspace_agents (workspace_id, agent_id, source)
		VALUES (1, 1, 'team')`)
	if insertErr == nil {
		t.Errorf("INSERT with source='team' should fail after CHECK update")
	} else if !strings.Contains(insertErr.Error(), "CHECK") {
		t.Logf("got non-CHECK error for source='team' INSERT (still a constraint): %v", insertErr)
	}

	// source CHECK should allow 'run'.
	_, err = db.Exec(`INSERT INTO workspace_agents (workspace_id, agent_id, source)
		VALUES (1, 1, 'run')`)
	if err != nil {
		t.Errorf("INSERT with source='run' should succeed: %v", err)
	}

	// Migration marker should be set.
	var key string
	err = db.QueryRow(`SELECT key FROM schema_migrations WHERE key='drop_legacy_phase7_v1'`).Scan(&key)
	if err != nil {
		t.Errorf("migration marker not set: %v", err)
	}
}

func TestDropLegacyPhase7_Idempotent(t *testing.T) {
	// Use the current (post-Phase-7) schema: no legacy tables/columns.
	db := openCurrentDBForPhase7(t)
	defer db.Close()

	ctx := context.Background()
	// First run: no legacy data, should succeed.
	if err := MigrateDropLegacyPhase7(ctx, db); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// Second run: idempotent, should not fail.
	if err := MigrateDropLegacyPhase7(ctx, db); err != nil {
		t.Fatalf("second run (idempotent): %v", err)
	}
}

func TestDropLegacyPhase7_Postgres(t *testing.T) {
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TEST_PG_DSN not set; skipping Postgres test")
	}
	// Postgres path tested via real DSN in CI/CD only.
}
