package store

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// TestMigrateAllowCursorCLIType simulates a DB whose workspaces.cli_type and
// projects.default_cli_type CHECK enums include 'goose' but predate 'cursor'.
// The migration must widen both enums (SQLite rebuild) so a cursor workspace can
// be created, while preserving existing rows and indexes.
func TestMigrateAllowCursorCLIType(t *testing.T) {
	original := Driver
	defer func() { Driver = original }()
	Driver = "sqlite"

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	// Post-goose schema: enums admit through 'goose', not yet 'cursor'.
	_, err = db.Exec(`
		CREATE TABLE claude_accounts (id INTEGER PRIMARY KEY AUTOINCREMENT);
		CREATE TABLE codex_accounts (id INTEGER PRIMARY KEY AUTOINCREMENT);
		CREATE TABLE workspaces (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			name           TEXT NOT NULL DEFAULT '',
			path           TEXT NOT NULL,
			status         TEXT NOT NULL DEFAULT 'created',
			cli_type       TEXT NOT NULL DEFAULT 'claude' CHECK (cli_type IN ('claude','codex','qwen','omp','goose')),
			created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX idx_workspaces_cli_type ON workspaces(cli_type);
		CREATE TABLE projects (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			name          TEXT NOT NULL,
			default_cli_type TEXT NOT NULL DEFAULT 'claude' CHECK (default_cli_type IN ('claude','codex','qwen','omp','goose'))
		);
		CREATE INDEX idx_projects_default_cli_type ON projects(default_cli_type);
		INSERT INTO workspaces(id, name, path, cli_type) VALUES (1, 'ws', '/p', 'goose');
		INSERT INTO projects(id, name) VALUES (1, 'proj');
	`)
	if err != nil {
		t.Fatal(err)
	}

	// Precondition: creating a cursor workspace fails on the legacy CHECK.
	if _, err := db.Exec(`INSERT INTO workspaces(name, path, cli_type) VALUES ('c', '/c', 'cursor')`); err == nil {
		t.Fatal("expected CHECK constraint to reject 'cursor' before migration")
	}

	migrateAllowCursorCLIType(db)

	// Both enums now admit 'cursor'.
	if _, err := db.Exec(`INSERT INTO workspaces(name, path, cli_type) VALUES ('c', '/c', 'cursor')`); err != nil {
		t.Fatalf("cursor workspace insert should succeed after migration: %v", err)
	}
	if _, err := db.Exec(`UPDATE projects SET default_cli_type='cursor' WHERE id=1`); err != nil {
		t.Fatalf("projects default_cli_type='cursor' should succeed after migration: %v", err)
	}

	// The previously-added enum values must survive the rebuild.
	if _, err := db.Exec(`INSERT INTO workspaces(name, path, cli_type) VALUES ('g2', '/g2', 'goose')`); err != nil {
		t.Fatalf("'goose' should still be admitted after the cursor widening: %v", err)
	}

	// Existing rows preserved.
	var cliType string
	if err := db.QueryRow(`SELECT cli_type FROM workspaces WHERE id=1`).Scan(&cliType); err != nil {
		t.Fatal(err)
	}
	if cliType != "goose" {
		t.Fatalf("existing workspace row not preserved: cli_type=%q", cliType)
	}

	// Indexes recreated.
	for _, idx := range []string{"idx_workspaces_cli_type", "idx_projects_default_cli_type"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("index %s not recreated after rebuild", idx)
		}
	}

	// Marker set → a second run is a no-op.
	migrateAllowCursorCLIType(db)
	if _, err := db.Exec(`INSERT INTO workspaces(name, path, cli_type) VALUES ('c2', '/c2', 'cursor')`); err != nil {
		t.Fatalf("second run should be a no-op and still admit 'cursor': %v", err)
	}
}
