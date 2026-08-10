package store

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// NOTE: TestMigrate_AddSubagentColumns was removed when the workspace_agents
// table was dropped (workflow / team_dispatch decommission, Plan 2 phase 5).

// TestMigrateIssueAssigneeLabelsAddTables verifies that the labels /
// issue_labels / issue_assignees tables are created on existing DBs and
// that the marker prevents re-runs (idempotency).
func TestMigrateIssueAssigneeLabelsAddTables(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	Driver = "sqlite"

	// Force re-run by clearing the marker.
	_, _ = db.Exec(`DELETE FROM schema_migrations WHERE key='issue_assignee_labels_v1'`)
	// Drop the new tables to simulate an old DB.
	for _, tbl := range []string{"issue_assignees", "issue_labels", "labels"} {
		_, _ = db.Exec(`DROP TABLE IF EXISTS ` + tbl)
	}

	migrateIssueAssigneeLabelsAddTables(db)

	for _, tbl := range []string{"labels", "issue_labels", "issue_assignees"} {
		var n int
		err := db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl,
		).Scan(&n)
		if err != nil || n != 1 {
			t.Errorf("table %s not created (n=%d, err=%v)", tbl, n, err)
		}
	}

	// Idempotency
	migrateIssueAssigneeLabelsAddTables(db)
}

// TestMigrateIssueAssigneeLabelsDropLegacyColumns verifies that the legacy
// issues.{labels,assignee} TEXT columns are dropped on existing DBs and that
// the marker prevents re-runs (idempotency). Re-creates the columns first to
// simulate an existing pre-phase-2 DB (the current schema.sql no longer has
// them since this is the post-phase-2 state).
func TestMigrateIssueAssigneeLabelsDropLegacyColumns(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	Driver = "sqlite"

	// Apply current schema (without legacy columns), then re-add them to mimic
	// a database that was on phase 1.
	if _, err := db.Exec(Schema); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE issues ADD COLUMN labels TEXT DEFAULT ''`); err != nil {
		t.Fatalf("re-add labels: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE issues ADD COLUMN assignee TEXT DEFAULT ''`); err != nil {
		t.Fatalf("re-add assignee: %v", err)
	}
	// Make sure the marker is unset.
	_, _ = db.Exec(`DELETE FROM schema_migrations WHERE key='issue_assignee_labels_drop_legacy_v1'`)

	migrateIssueAssigneeLabelsDropLegacyColumns(db)

	for _, col := range []string{"labels", "assignee"} {
		exists, err := columnExists(db, "issues", col)
		if err != nil {
			t.Fatalf("columnExists %s: %v", col, err)
		}
		if exists {
			t.Errorf("issues.%s still present after drop migration", col)
		}
	}

	// Idempotency: second call is a no-op.
	migrateIssueAssigneeLabelsDropLegacyColumns(db)
}
