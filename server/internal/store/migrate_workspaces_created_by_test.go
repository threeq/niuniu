package store

import (
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// openSchemaSeededDB returns a fresh in-memory SQLite DB with the canonical
// schema applied. This matches production reality: schema.sql ran first, then
// Migrate ran on top. Tests that exercise migration code paths should use
// this rather than hand-rolled minimal CREATE TABLE statements — the latter
// breaks any rebuild migration that assumes canonical columns exist.
func openSchemaSeededDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(0)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(Schema); err != nil {
		t.Fatalf("apply Schema: %v", err)
	}
	return db
}

func TestMigrate_WorkspacesCreatedByBackfill(t *testing.T) {
	db := openSchemaSeededDB(t)

	// Seed legacy-shaped rows: created_by IS NULL on every row (just as
	// upgraded DBs look right after addColumnIfNotExists adds the column).
	if _, err := db.Exec(
		`INSERT INTO workspaces (id, name, path, owner_type, owner_id) VALUES
			(1, 'a', '/a', 'user', 42),
			(2, 'b', '/b', 'org',  7),
			(3, 'c', '/c', 'user', 99)`); err != nil {
		t.Fatal(err)
	}

	Migrate(db)

	rows, err := db.Query(`SELECT id, owner_type, owner_id, created_by FROM workspaces ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	type row struct {
		id, ownerID int64
		ownerType   string
		createdBy   sql.NullInt64
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.ownerType, &r.ownerID, &r.createdBy); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}

	if len(got) != 3 {
		t.Fatalf("want 3 rows, got %d", len(got))
	}
	if !got[0].createdBy.Valid || got[0].createdBy.Int64 != 42 {
		t.Errorf("row 1 created_by: want 42, got %v", got[0].createdBy)
	}
	if got[1].createdBy.Valid {
		t.Errorf("row 2 created_by: want NULL, got %v", got[1].createdBy)
	}
	if !got[2].createdBy.Valid || got[2].createdBy.Int64 != 99 {
		t.Errorf("row 3 created_by: want 99, got %v", got[2].createdBy)
	}

	// Idempotency: re-running Migrate must not re-backfill.
	if _, err := db.Exec(`UPDATE workspaces SET created_by = NULL WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	Migrate(db)
	var second sql.NullInt64
	if err := db.QueryRow(`SELECT created_by FROM workspaces WHERE id=1`).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if second.Valid {
		t.Errorf("idempotency violated: row 1 created_by re-backfilled")
	}
}

// TestReconcileWorkspacesFKsSQLite_Rebuilds covers the upgrade path: a DB
// where workspaces.created_by was added by addColumnIfNotExists *without* a
// REFERENCES clause (because ALTER TABLE ADD COLUMN doesn't support adding
// FK constraints in SQLite). The reconciliation should detect the missing
// FK, rebuild the table with the canonical FK clause, preserve all rows and
// indexes, and set the idempotent marker.
func TestReconcileWorkspacesFKsSQLite_Rebuilds(t *testing.T) {
	db := openSchemaSeededDB(t)

	// Simulate a pre-feature DB: drop the index that references created_by
	// (SQLite refuses to drop a column referenced by an index), then drop
	// created_by, then re-add it WITHOUT a REFERENCES clause — exactly the
	// shape addColumnIfNotExists produces on upgrade paths. SQLite 3.35+
	// supports DROP COLUMN; modernc.org/sqlite is recent enough.
	if _, err := db.Exec(`DROP INDEX IF EXISTS idx_workspaces_created_by`); err != nil {
		t.Fatalf("drop index: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE workspaces DROP COLUMN created_by`); err != nil {
		t.Fatalf("drop created_by: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE workspaces ADD COLUMN created_by INTEGER DEFAULT NULL`); err != nil {
		t.Fatalf("re-add created_by without FK: %v", err)
	}
	if _, err := db.Exec(`CREATE INDEX idx_workspaces_created_by ON workspaces(created_by)`); err != nil {
		t.Fatalf("recreate index: %v", err)
	}
	// idx_workspaces_owner is created by addOwnerModel in Migrate(); we
	// add it manually here to mirror production state where Migrate has
	// already run before reconciliation kicks in.
	if _, err := db.Exec(
		`CREATE INDEX IF NOT EXISTS idx_workspaces_owner ON workspaces(owner_type, owner_id)`); err != nil {
		t.Fatalf("create idx_workspaces_owner: %v", err)
	}

	// Seed a couple of rows so we can verify data preservation.
	if _, err := db.Exec(`INSERT INTO users (id, username, password_hash, display_name) VALUES
		(11, 'alice', 'x', 'Alice'),
		(12, 'bob', 'x', 'Bob')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO workspaces (id, name, path, owner_type, owner_id, created_by) VALUES
			(101, 'w1', '/w1', 'user', 11, 11),
			(102, 'w2', '/w2', 'org',  5,  NULL)`); err != nil {
		t.Fatal(err)
	}

	// Sanity: the DDL right now has no FK on created_by.
	var beforeDDL string
	if err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='workspaces'`,
	).Scan(&beforeDDL); err != nil {
		t.Fatalf("read DDL before: %v", err)
	}
	if strings.Contains(beforeDDL, "created_by") &&
		strings.Contains(beforeDDL, "REFERENCES users(id) ON DELETE SET NULL") &&
		// The FK clause must follow the created_by declaration; rough proximity check.
		// `current_session_user_id` and `created_by` both REFERENCES users(id) ON
		// DELETE SET NULL, so a substring search alone isn't enough — but for the
		// `before` state we expect the FK to be entirely absent for created_by.
		// Conservative check: count occurrences of the canonical phrase. After
		// DROP+ADD, only current_session_user_id should carry it.
		strings.Count(beforeDDL, "REFERENCES users(id) ON DELETE SET NULL") > 1 {
		t.Fatalf("setup failed: created_by FK already present\nDDL: %s", beforeDDL)
	}

	// Run reconciliation.
	reconcileWorkspacesFKsSQLite(db)

	// DDL after must include the canonical FK clause for created_by.
	var afterDDL string
	if err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='workspaces'`,
	).Scan(&afterDDL); err != nil {
		t.Fatalf("read DDL after: %v", err)
	}
	if !strings.Contains(afterDDL, "created_by") {
		t.Fatalf("created_by missing post-rebuild\nDDL: %s", afterDDL)
	}
	// At least two REFERENCES users(id) ON DELETE SET NULL occurrences:
	// current_session_user_id and created_by both carry it.
	if got := strings.Count(afterDDL, "REFERENCES users(id) ON DELETE SET NULL"); got < 2 {
		t.Fatalf("FK clause count: want >= 2 (current_session_user_id + created_by), got %d\nDDL: %s", got, afterDDL)
	}

	// Verify FK is enforced live: deleting user 11 should NULL row 101's created_by.
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("enable FK: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM users WHERE id = 11`); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	var got101 sql.NullInt64
	if err := db.QueryRow(
		`SELECT created_by FROM workspaces WHERE id = 101`).Scan(&got101); err != nil {
		t.Fatalf("read row 101: %v", err)
	}
	if got101.Valid {
		t.Errorf("row 101 created_by: ON DELETE SET NULL didn't fire; got %v", got101.Int64)
	}

	// Verify data preservation: row 102's NULL stays NULL, row 102 itself
	// still exists.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspaces`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("row count: want 2, got %d", count)
	}
	var got102 sql.NullInt64
	if err := db.QueryRow(
		`SELECT created_by FROM workspaces WHERE id = 102`).Scan(&got102); err != nil {
		t.Fatalf("read row 102: %v", err)
	}
	if got102.Valid {
		t.Errorf("row 102 created_by: was NULL pre-rebuild, expected NULL post-rebuild; got %v", got102.Int64)
	}

	// Verify indexes recreated. idx_workspaces_owner and idx_workspaces_created_by
	// are the two declared indexes; both should be back.
	for _, idx := range []string{"idx_workspaces_owner", "idx_workspaces_created_by"} {
		var name string
		if err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='workspaces' AND name = ?`,
			idx,
		).Scan(&name); err != nil {
			t.Errorf("expected index %q to exist post-rebuild: %v", idx, err)
		}
	}

	// Idempotency: re-running reconciliation must be a no-op (DDL unchanged).
	reconcileWorkspacesFKsSQLite(db)
	var rerunDDL string
	if err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='workspaces'`,
	).Scan(&rerunDDL); err != nil {
		t.Fatal(err)
	}
	if rerunDDL != afterDDL {
		t.Error("idempotency violated: DDL changed on second reconciliation run")
	}
}
