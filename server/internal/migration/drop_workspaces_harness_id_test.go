package migration

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// TestDropWorkspacesHarnessID_DropsDanglingColumn reproduces the bug
// observed on user DBs that ran Phase 7 before it learned to drop
// workspaces.harness_id: the column persists and references the
// already-dropped harnesses table, so PRAGMA foreign_keys=ON makes
// every workspace INSERT fail with "no such table: main.harnesses".
//
// The migration must drop the dangling column and mark itself done.
func TestDropWorkspacesHarnessID_DropsDanglingColumn(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Build a minimal workspaces table that mirrors the broken shape on
	// affected user DBs: harness_id INTEGER REFERENCES harnesses(id)
	// with the harnesses table itself absent.
	if _, err := db.Exec(`
		CREATE TABLE workspaces (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			name        TEXT NOT NULL DEFAULT '',
			path        TEXT NOT NULL,
			harness_id  INTEGER DEFAULT NULL REFERENCES harnesses(id)
		)`); err != nil {
		t.Fatalf("seed workspaces: %v", err)
	}

	// Sanity: with foreign_keys ON, INSERT into workspaces should fail
	// because the FK target table is missing — this is the prod symptom.
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("enable fk: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces (name, path) VALUES ('pre', '/tmp/pre')`); err == nil {
		t.Fatal("pre-migration INSERT unexpectedly succeeded")
	}

	// Apply the migration twice to confirm idempotency.
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := MigrateDropWorkspacesHarnessID(ctx, db); err != nil {
			t.Fatalf("migration iter %d: %v", i, err)
		}
	}

	// Confirm the column is gone.
	exists, err := columnExistsP7(db, "workspaces", "harness_id")
	if err != nil {
		t.Fatalf("columnExistsP7: %v", err)
	}
	if exists {
		t.Fatal("workspaces.harness_id still present after migration")
	}

	// Confirm INSERT now succeeds.
	if _, err := db.Exec(`INSERT INTO workspaces (name, path) VALUES ('post', '/tmp/post')`); err != nil {
		t.Fatalf("post-migration INSERT failed: %v", err)
	}

	// Confirm marker row is present.
	var marker string
	if err := db.QueryRow(
		`SELECT key FROM schema_migrations WHERE key = ?`,
		"drop_workspaces_harness_id_v1").Scan(&marker); err != nil {
		t.Fatalf("marker check: %v", err)
	}
}

// TestDropWorkspacesHarnessID_NoOpWhenColumnAbsent: a fresh DB without
// the column at all (post-fix Phase 7 path) must not error.
func TestDropWorkspacesHarnessID_NoOpWhenColumnAbsent(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`
		CREATE TABLE workspaces (
			id   INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL DEFAULT ''
		)`); err != nil {
		t.Fatalf("seed workspaces: %v", err)
	}

	if err := MigrateDropWorkspacesHarnessID(context.Background(), db); err != nil {
		t.Fatalf("migration: %v", err)
	}
}
