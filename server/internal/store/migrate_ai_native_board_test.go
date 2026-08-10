package store_test

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/niuniu-dev/niuniu/internal/pgtest"
	"github.com/niuniu-dev/niuniu/internal/store"
	_ "modernc.org/sqlite"
)

// openSchemaOnly opens a DB for the given driver with the base schema applied but
// BEFORE store.Migrate, so a test can seed legacy rows that predate the stage-1a
// columns and then exercise the add + backfill in a single Migrate pass (exactly
// like a real upgrade). The PG branch auto-skips when Docker / NIUNIU_TEST_PG_DSN
// is unavailable (inside pgtest.NewSchemaDSN -> PostgresDSN).
//
// We exec the raw schema string (store.Schema / store.SchemaPostgres) rather than
// store.ApplySchema: ApplySchema also runs the open.go per-table column-add
// migrations, whose columnExists() probe hardcodes table_schema='public' and so
// misfires under the per-test custom search_path schema NewSchemaDSN creates
// (it would try to re-ADD lifecycle_status and hit SQLSTATE 42701). The raw CREATE
// blocks already contain every base column, so the fresh-DB schema is complete.
// This mirrors the proven openLegacyHarnessScopeDB pattern in this package.
func openSchemaOnly(t *testing.T, drv string) *sql.DB {
	t.Helper()
	var db *sql.DB
	var schema string
	switch drv {
	case pgtest.DriverSQLite:
		var err error
		db, err = sql.Open("sqlite", ":memory:?_journal_mode=WAL&_busy_timeout=5000")
		if err != nil {
			t.Fatalf("open sqlite: %v", err)
		}
		store.Driver = "sqlite"
		schema = store.Schema
	case pgtest.DriverPostgres:
		dsn := pgtest.NewSchemaDSN(t)
		var err error
		db, err = sql.Open("pgx", dsn)
		if err != nil {
			t.Fatalf("open pgx: %v", err)
		}
		store.Driver = "postgres"
		schema = store.SchemaPostgres
	default:
		t.Fatalf("unknown driver %q", drv)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("apply %s schema: %v", drv, err)
	}
	return db
}

// TestMigrateAINativeBoardStage1a exercises stage 1a end-to-end on both SQLite
// and PostgreSQL (spec 2026-06-05-ai-native-board-execution-design.md §11/§17):
//   - columns.op_primitive / columns.when_to_use and column_gate_specs.applicability
//     are added with the right defaults + CHECK constraints;
//   - op_primitive is backfilled from lifecycle_mapping (NOT left at bare 'none');
//   - in-transit running epics are reconciled to 'paused' while running children
//     are left alone;
//   - the whole migration parses and runs cleanly on PG ("不崩库") and is idempotent.
func TestMigrateAINativeBoardStage1a(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		db := openSchemaOnly(t, drv)
		w := store.Wrap(db)
		ctx := context.Background()

		mustExec(t, w, ctx, `INSERT INTO projects (id, name) VALUES (1, 'p')`)

		// Legacy 6-column CreateProjectWithDefaults shape (kanban.go) plus an empty
		// and a spec/plan-only column. Seeded with explicit ids BEFORE Migrate, so
		// they predate op_primitive just like an upgraded DB.
		seed := []struct{ name, mapping, want string }{
			{"Backlog", "created", "none"},
			{"SpecPlan", "spec,spec-review,plan,plan-review", "instruct"},
			{"Implement", "implement", "instruct"},
			{"Review", "implement-review", "instruct"},
			{"Test", "test", "instruct"},
			{"Done", "completed", "complete"},
			{"Empty", "", "none"},
			{"PlanOnly", "spec,plan", "none"},
		}
		for i, s := range seed {
			mustExec(t, w, ctx,
				`INSERT INTO columns (id, project_id, name, position, lifecycle_mapping) VALUES (?, 1, ?, ?, ?)`,
				i+1, s.name, i, s.mapping)
		}

		// In-transit epic left 'running' (zombie after restart) + a running child.
		mustExec(t, w, ctx,
			`INSERT INTO issues (id, column_id, title, issue_type, exec_status) VALUES (100, 1, 'epic', 'epic', 'running')`)
		mustExec(t, w, ctx,
			`INSERT INTO issues (id, column_id, title, issue_type, exec_status, parent_issue_id) VALUES (101, 1, 'child', 'task', 'running', 100)`)

		store.Migrate(db)
		store.Migrate(db) // idempotent: second pass must be a no-op (markers set)

		// op_primitive backfilled from lifecycle_mapping.
		for _, s := range seed {
			var got string
			mustScan(t, w, ctx, `SELECT op_primitive FROM columns WHERE name = ?`, []any{s.name}, &got)
			if got != s.want {
				t.Fatalf("column %q (mapping=%q): op_primitive=%q, want %q", s.name, s.mapping, got, s.want)
			}
		}

		// A freshly inserted column gets op_primitive='none', when_to_use NULL.
		mustExec(t, w, ctx, `INSERT INTO columns (id, project_id, name) VALUES (999, 1, 'fresh')`)
		var op string
		var when sql.NullString
		mustScan(t, w, ctx, `SELECT op_primitive, when_to_use FROM columns WHERE id = 999`, nil, &op, &when)
		if op != "none" {
			t.Fatalf("fresh column op_primitive=%q, want 'none'", op)
		}
		if when.Valid {
			t.Fatalf("fresh column when_to_use=%q, want NULL", when.String)
		}

		// op_primitive CHECK rejects an out-of-set value.
		if _, err := w.ExecContext(ctx,
			`INSERT INTO columns (id, project_id, name, op_primitive) VALUES (998, 1, 'bad', 'frobnicate')`); err == nil {
			t.Fatalf("expected CHECK violation on op_primitive='frobnicate'")
		}

		// column_gate_specs.applicability default + CHECK. Bind to a global spec.
		mustExec(t, w, ctx, `INSERT INTO harness_specs (id, category, name) VALUES (1, 'quality', 's1')`)
		mustExec(t, w, ctx, `INSERT INTO column_gate_specs (column_id, spec_id, position) VALUES (1, 1, 0)`)
		var app string
		mustScan(t, w, ctx, `SELECT applicability FROM column_gate_specs WHERE column_id = 1 AND spec_id = 1`, nil, &app)
		if app != "if_routed" {
			t.Fatalf("applicability default=%q, want 'if_routed'", app)
		}
		if _, err := w.ExecContext(ctx,
			`INSERT INTO column_gate_specs (column_id, spec_id, position, applicability) VALUES (2, 1, 0, 'whenever')`); err == nil {
			t.Fatalf("expected CHECK violation on applicability='whenever'")
		}

		// Epic in-transit收口: running epic -> paused; running child untouched.
		var epicStatus, childStatus string
		mustScan(t, w, ctx, `SELECT exec_status FROM issues WHERE id = 100`, nil, &epicStatus)
		mustScan(t, w, ctx, `SELECT exec_status FROM issues WHERE id = 101`, nil, &childStatus)
		if epicStatus != "paused" {
			t.Fatalf("epic exec_status=%q after drain, want 'paused'", epicStatus)
		}
		if childStatus != "running" {
			t.Fatalf("child exec_status=%q, want untouched 'running'", childStatus)
		}

		// No-clobber: a user edit must survive a later Migrate (marker prevents re-run).
		mustExec(t, w, ctx, `UPDATE columns SET op_primitive = 'complete' WHERE name = 'Backlog'`)
		store.Migrate(db)
		var after string
		mustScan(t, w, ctx, `SELECT op_primitive FROM columns WHERE name = 'Backlog'`, nil, &after)
		if after != "complete" {
			t.Fatalf("backfill re-ran and clobbered user edit: got %q, want 'complete'", after)
		}
	})
}

// TestMigrateAINativeBoardStage4 exercises stage 4's data-model delta on both
// drivers (spec §11.3/§22.4): the issues.floor_retry_count column is added with a 0
// default, and issue.exec_status accepts the new 'gate_checking'/'gate_blocked'
// states (the floor gate's blocked-state landing). The migration parses + runs
// cleanly on PG and is idempotent.
func TestMigrateAINativeBoardStage4(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		db := openSchemaOnly(t, drv)
		w := store.Wrap(db)
		ctx := context.Background()

		mustExec(t, w, ctx, `INSERT INTO projects (id, name) VALUES (1, 'p')`)
		mustExec(t, w, ctx, `INSERT INTO columns (id, project_id, name) VALUES (1, 1, 'done')`)
		// Seed an issue BEFORE Migrate so floor_retry_count is added to an existing row.
		mustExec(t, w, ctx, `INSERT INTO issues (id, column_id, title) VALUES (200, 1, 'i')`)

		store.Migrate(db)
		store.Migrate(db) // idempotent

		// floor_retry_count added with default 0.
		var retry int
		mustScan(t, w, ctx, `SELECT floor_retry_count FROM issues WHERE id = 200`, nil, &retry)
		if retry != 0 {
			t.Fatalf("floor_retry_count default=%d, want 0", retry)
		}
		mustExec(t, w, ctx, `UPDATE issues SET floor_retry_count = floor_retry_count + 1 WHERE id = 200`)
		mustScan(t, w, ctx, `SELECT floor_retry_count FROM issues WHERE id = 200`, nil, &retry)
		if retry != 1 {
			t.Fatalf("floor_retry_count after incr=%d, want 1", retry)
		}

		// exec_status accepts the new floor-gate states (fresh-DB CHECK widened).
		for _, st := range []string{"gate_checking", "gate_blocked"} {
			if _, err := w.ExecContext(ctx, `UPDATE issues SET exec_status = ? WHERE id = 200`, st); err != nil {
				t.Fatalf("set exec_status=%q: %v", st, err)
			}
			var got string
			mustScan(t, w, ctx, `SELECT exec_status FROM issues WHERE id = 200`, nil, &got)
			if got != st {
				t.Fatalf("exec_status=%q, want %q", got, st)
			}
		}
	})
}

func mustExec(t *testing.T, w *store.DB, ctx context.Context, q string, args ...any) {
	t.Helper()
	if _, err := w.ExecContext(ctx, q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

// TestMigrateAINativeBoardStage7 exercises the stage-7 data model on both drivers
// (spec section 19/23.7): the exec_status_reason column + issue_exec_events table
// are created, and a fresh DB's exec_status CHECK accepts the two new terminal
// states (waiting_input / abandoned). Idempotent on a second pass.
func TestMigrateAINativeBoardStage7(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		db := openSchemaOnly(t, drv)
		w := store.Wrap(db)
		ctx := context.Background()

		store.Migrate(db)
		store.Migrate(db) // idempotent: markers set, second pass is a no-op

		mustExec(t, w, ctx, `INSERT INTO projects (id, name) VALUES (1, 'p')`)
		mustExec(t, w, ctx, `INSERT INTO columns (id, project_id, name, position) VALUES (1, 1, 'todo', 0)`)

		// Fresh-DB CHECK accepts the two new terminal states + records a reason.
		mustExec(t, w, ctx,
			`INSERT INTO issues (id, column_id, title, exec_status) VALUES (1, 1, 'a', 'waiting_input')`)
		mustExec(t, w, ctx,
			`INSERT INTO issues (id, column_id, title, exec_status, exec_status_reason) VALUES (2, 1, 'b', 'abandoned', 'not my job')`)

		// issue_exec_events table exists and accepts an append.
		mustExec(t, w, ctx,
			`INSERT INTO issue_exec_events (issue_id, kind, summary) VALUES (1, 'advance', 'moved')`)

		var n int
		mustScan(t, w, ctx, `SELECT COUNT(*) FROM issue_exec_events WHERE issue_id = ?`, []any{1}, &n)
		if n != 1 {
			t.Errorf("issue_exec_events count = %d, want 1", n)
		}
		var reason string
		mustScan(t, w, ctx, `SELECT exec_status_reason FROM issues WHERE id = ?`, []any{2}, &reason)
		if reason != "not my job" {
			t.Errorf("exec_status_reason = %q, want 'not my job'", reason)
		}
	})
}

// mustScan runs q with the given args and scans the single result row into dest.
// args may be nil for a no-arg query.
func mustScan(t *testing.T, w *store.DB, ctx context.Context, q string, args []any, dest ...any) {
	t.Helper()
	if err := w.QueryRowContext(ctx, q, args...).Scan(dest...); err != nil {
		t.Fatalf("scan %q: %v", q, err)
	}
}
