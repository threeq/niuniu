package store

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// applySchemaOnly opens an in-memory DB and applies the embedded schema WITHOUT
// running Migrate, so a migration under test starts unmarked.
func applySchemaOnly(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	Driver = "sqlite"
	if err := ApplySchema(db); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return db
}

func TestMigrateLearningsToMemory(t *testing.T) {
	db := applySchemaOnly(t)
	ctx := context.Background()

	// project_learnings is no longer in the schema (dropped in #256), so recreate
	// the legacy table here to simulate an existing DB that still has data to
	// migrate. Mirrors the pre-#256 definition.
	if _, err := db.Exec(`CREATE TABLE project_learnings (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		project_id   INTEGER NOT NULL,
		category     TEXT NOT NULL,
		title        TEXT NOT NULL,
		content      TEXT NOT NULL DEFAULT '',
		source       TEXT NOT NULL DEFAULT 'manual',
		workspace_id INTEGER,
		created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatalf("create legacy project_learnings: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO projects (name, owner_type, owner_id) VALUES ('p256', 'org', 7)`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	var projectID int64
	if err := db.QueryRow(`SELECT id FROM projects WHERE name = 'p256'`).Scan(&projectID); err != nil {
		t.Fatalf("project id: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO project_learnings (project_id, category, title, content, source) VALUES (?, 'gotcha', 'pgx 42P18', 'anchor every ? to a typed column', 'mcp')`,
		projectID); err != nil {
		t.Fatalf("seed learning: %v", err)
	}

	migrateLearningsToMemory(db)

	// Memory row created, owner inherited from project, category -> mem_type.
	var (
		ownerType, memType, title, content, source string
		ownerID, version                           int64
		pid                                        sql.NullInt64
	)
	row := db.QueryRow(`SELECT owner_type, owner_id, project_id, mem_type, title, content, source, version FROM memories`)
	if err := row.Scan(&ownerType, &ownerID, &pid, &memType, &title, &content, &source, &version); err != nil {
		t.Fatalf("memory not created: %v", err)
	}
	if ownerType != "org" || ownerID != 7 {
		t.Errorf("owner not inherited from project: got %s/%d", ownerType, ownerID)
	}
	if memType != "gotcha" || title != "pgx 42P18" || source != "mcp" || version != 1 {
		t.Errorf("unexpected memory fields: %s/%s/%s/v%d", memType, title, source, version)
	}
	if !pid.Valid || pid.Int64 != projectID {
		t.Errorf("project_id not carried over: %+v", pid)
	}

	// v1 snapshot exists.
	var vcount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memory_versions WHERE version = 1`).Scan(&vcount); err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if vcount != 1 {
		t.Errorf("expected 1 v1 snapshot, got %d", vcount)
	}

	// Idempotent: re-running must not duplicate (NOT EXISTS guard + marker).
	migrateLearningsToMemory(db)
	var mcount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memories`).Scan(&mcount); err != nil {
		t.Fatalf("count memories: %v", err)
	}
	if mcount != 1 {
		t.Errorf("migration not idempotent: got %d memories", mcount)
	}
	_ = ctx
}

func TestDropProjectLearningsTable(t *testing.T) {
	db := applySchemaOnly(t)

	// Schema no longer creates it.
	if projectLearningsTableExists(db) {
		t.Fatal("fresh schema should not have project_learnings")
	}
	// Drop is a safe no-op when absent.
	dropProjectLearningsTable(db)

	// Simulate a legacy DB and drop it. (Use a fresh schema_migrations-free DB
	// by recreating the table; the marker from the no-op above guards re-runs,
	// so assert the DROP path via a second DB.)
	db2 := applySchemaOnly(t)
	if _, err := db2.Exec(`CREATE TABLE project_learnings (id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if !projectLearningsTableExists(db2) {
		t.Fatal("legacy table should exist before drop")
	}
	dropProjectLearningsTable(db2)
	if projectLearningsTableExists(db2) {
		t.Fatal("project_learnings should be dropped")
	}
	// Idempotent: re-run must not error.
	dropProjectLearningsTable(db2)
}

func TestMigrateLearningsToMemory_NoTableNoOp(t *testing.T) {
	db := applySchemaOnly(t) // no project_learnings table
	// Must not error / panic and must mark itself done.
	migrateLearningsToMemory(db)
	if !migrationApplied(Wrap(db), "learnings_to_memory_v1") {
		t.Error("migration should be marked applied even with no legacy table")
	}
}
