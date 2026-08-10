package store

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// TestMigrateSavedQueriesSourceNullable_SQLiteRebuild simulates a legacy DB
// where saved_queries.source_id is still NOT NULL (the pre-static-chart
// schema). The migration must rebuild the table so a static chart (source_id
// = NULL) can be inserted, while preserving existing rows and indexes.
func TestMigrateSavedQueriesSourceNullable_SQLiteRebuild(t *testing.T) {
	original := Driver
	defer func() { Driver = original }()
	Driver = "sqlite"

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	// Legacy schema: source_id NOT NULL, no snapshot column, with the two
	// indexes the live DB carries.
	_, err = db.Exec(`
		CREATE TABLE data_sources (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT);
		CREATE TABLE workspaces (id INTEGER PRIMARY KEY AUTOINCREMENT);
		CREATE TABLE saved_queries (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			owner_type   TEXT NOT NULL DEFAULT 'user',
			owner_id     INTEGER NOT NULL DEFAULT 0,
			source_id    INTEGER NOT NULL REFERENCES data_sources(id) ON DELETE CASCADE,
			workspace_id INTEGER,
			name         TEXT NOT NULL,
			operation    TEXT NOT NULL DEFAULT '{}',
			chart_spec   TEXT NOT NULL DEFAULT '{}',
			created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX idx_saved_queries_owner ON saved_queries(owner_type, owner_id);
		CREATE INDEX idx_saved_queries_source ON saved_queries(source_id);
		INSERT INTO data_sources(id, name) VALUES (7, 'analytics');
		INSERT INTO saved_queries(id, source_id, name) VALUES (1, 7, 'existing-query');
	`)
	if err != nil {
		t.Fatal(err)
	}

	// addColumnIfNotExists would normally add snapshot just before the
	// migration; do it here so the rebuild's copy carries the column.
	addColumnIfNotExists(db, "saved_queries", "snapshot", "TEXT NOT NULL DEFAULT ''")

	// Precondition: a NULL source_id insert fails on the legacy table.
	if _, err := db.Exec(`INSERT INTO saved_queries(source_id, name) VALUES (NULL, 'static-pre')`); err == nil {
		t.Fatal("expected NOT NULL constraint to reject NULL source_id before migration")
	}

	if err := migrateSavedQueriesSourceNullable(db); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	// source_id must now be nullable.
	var notNull int
	if err := db.QueryRow(`SELECT "notnull" FROM pragma_table_info('saved_queries') WHERE name='source_id'`).Scan(&notNull); err != nil {
		t.Fatal(err)
	}
	if notNull != 0 {
		t.Fatalf("source_id still NOT NULL after migration (notnull=%d)", notNull)
	}

	// A static chart (NULL source_id) now inserts successfully.
	if _, err := db.Exec(`INSERT INTO saved_queries(source_id, name, snapshot) VALUES (NULL, 'static-chart', '{"result":null}')`); err != nil {
		t.Fatalf("static insert (NULL source_id) should succeed after migration: %v", err)
	}

	// Existing row preserved.
	var name string
	var srcID sql.NullInt64
	if err := db.QueryRow(`SELECT name, source_id FROM saved_queries WHERE id=1`).Scan(&name, &srcID); err != nil {
		t.Fatal(err)
	}
	if name != "existing-query" || !srcID.Valid || srcID.Int64 != 7 {
		t.Fatalf("existing row not preserved: name=%q source_id=%v", name, srcID)
	}

	// Both indexes recreated.
	for _, idx := range []string{"idx_saved_queries_owner", "idx_saved_queries_source"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("index %s not recreated after rebuild", idx)
		}
	}

	// Idempotent: a second run is a no-op (already nullable).
	if err := migrateSavedQueriesSourceNullable(db); err != nil {
		t.Fatalf("second migration run should be a no-op: %v", err)
	}
}
