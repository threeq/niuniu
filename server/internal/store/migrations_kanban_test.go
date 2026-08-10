package store

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(Schema); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return db
}

func TestMigrateColumnsExtensionFields(t *testing.T) {
	Driver = "sqlite"
	db := openTestDB(t)
	Migrate(db)
	for _, col := range []string{"reviewer_agent", "phase_prompt", "auto_advance"} {
		exists, err := columnExists(db, "columns", col)
		if err != nil {
			t.Fatalf("check %s: %v", col, err)
		}
		if !exists {
			t.Fatalf("column columns.%s missing after Migrate", col)
		}
	}
	// executor_agent is retired: Migrate drops it from existing DBs.
	if exists, err := columnExists(db, "columns", "executor_agent"); err != nil {
		t.Fatalf("check executor_agent: %v", err)
	} else if exists {
		t.Fatalf("columns.executor_agent should be dropped after Migrate")
	}
	// Ensure default for auto_advance is 0 (so existing rows stay valid)
	if _, err := db.Exec("INSERT INTO projects (name, owner_type, owner_id) VALUES ('p', 'user', 1)"); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	var pid int64
	if err := db.QueryRow("SELECT id FROM projects WHERE name='p'").Scan(&pid); err != nil {
		t.Fatalf("get pid: %v", err)
	}
	if _, err := db.Exec("INSERT INTO columns (project_id, name) VALUES (?, 'todo')", pid); err != nil {
		t.Fatalf("insert column with defaults: %v", err)
	}
	var auto int
	if err := db.QueryRow("SELECT auto_advance FROM columns WHERE project_id=?", pid).Scan(&auto); err != nil {
		t.Fatalf("read auto_advance: %v", err)
	}
	if auto != 0 {
		t.Fatalf("expected auto_advance=0, got %d", auto)
	}
}

func TestMigrateColumnGateSpecsTable(t *testing.T) {
	Driver = "sqlite"
	db := openTestDB(t)
	Migrate(db)
	for _, col := range []string{"column_id", "spec_id", "position"} {
		exists, err := columnExists(db, "column_gate_specs", col)
		if err != nil {
			t.Fatalf("check %s: %v", col, err)
		}
		if !exists {
			t.Fatalf("column_gate_specs.%s missing", col)
		}
	}
}

// NOTE: Tests for project_templates / project_template_columns /
// issues.default_template_id / harness_runs / gate_jobs / GetActiveRunForIssue
// were removed when those tables/columns/queries were dropped (workflow /
// template-run decommission, Plan 2 phase 5).
