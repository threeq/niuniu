package migration

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// columnExistsWF reports whether a column exists on a table (SQLite only).
func columnExistsWF(db *sql.DB, table, col string) (bool, error) {
	var count int
	err := db.QueryRow(
		"SELECT COUNT(*) FROM pragma_table_info('"+table+"') WHERE name = ?", col,
	).Scan(&count)
	return count > 0, err
}

// deadWorkflowIndexes lists the now-unused indexes on the retained-but-dead FK
// columns. They were created for the (now decommissioned) workflow subsystem;
// nothing queries these columns anymore. On SQLite they must be dropped before
// ALTER TABLE DROP COLUMN, which otherwise fails with "error in index <name>
// after drop column". On Postgres DROP COLUMN auto-removes dependent indexes,
// but we drop them explicitly so existing PG installs don't keep dead indexes.
// All drops use IF EXISTS so they are safe whether or not the index is present.
var deadWorkflowIndexes = []string{
	"idx_agent_messages_harness_run", // agent_messages.harness_run_id
	"idx_harness_checks_run",         // harness_checks.run_id
}

// dropDeadWorkflowIndexes removes every index in deadWorkflowIndexes, checking
// existence via IF EXISTS so it is idempotent on both drivers.
func dropDeadWorkflowIndexes(ctx context.Context, raw *sql.DB) error {
	for _, idx := range deadWorkflowIndexes {
		if _, err := raw.ExecContext(ctx, "DROP INDEX IF EXISTS "+idx); err != nil {
			return fmt.Errorf("drop index %s: %w", idx, err)
		}
	}
	return nil
}

// MigrateDropWorkflowTables drops the legacy workflow / template-run tables that
// were decommissioned in Plan 2 (workflow + team_dispatch + template-run
// subsystems). Tables dropped:
//
//	project_templates, project_template_columns, harness_runs, gate_jobs,
//	workspace_agents, workspace_agent_repos, project_preset_imports
//
// Before dropping, all foreign keys that point at the dropped tables are
// detached. The columns themselves are retained (now plain integers, no FK):
//
//	issues.default_template_id          -> column dropped (no longer modelled)
//	agent_messages.harness_run_id       -> FK detached, column kept
//	workspace_costs.harness_run_id      -> FK detached, column kept
//	workspace_tasks.harness_run_id      -> FK detached, column kept
//	blackboard_entries.harness_run_id   -> FK detached, column kept
//	harness_checks.run_id               -> FK detached, column kept
//
// Idempotent: the schema_migrations key 'drop_workflow_tables_v1' prevents
// re-execution. Driver-aware: SQLite uses ALTER TABLE DROP COLUMN (3.35+) and
// DROP TABLE; PostgreSQL uses DROP CONSTRAINT / DROP COLUMN / DROP TABLE CASCADE.
//
// Historical data in the dropped tables has no retained value (the runtime
// consumers were removed in the preceding Plan-2 phases), so the tables are
// dropped outright rather than archived.
func MigrateDropWorkflowTables(ctx context.Context, raw *sql.DB) error {
	db := store.Wrap(raw)

	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		key TEXT PRIMARY KEY,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}
	var dummy string
	err := db.QueryRowContext(ctx, `SELECT key FROM schema_migrations WHERE key = ?`,
		"drop_workflow_tables_v1").Scan(&dummy)
	if err == nil {
		return nil // already applied
	}
	if err != sql.ErrNoRows {
		return fmt.Errorf("check migration marker: %w", err)
	}

	if store.Driver == "postgres" {
		if err := migrateDropWorkflowTablesPostgres(ctx, raw); err != nil {
			return err
		}
	} else {
		if err := migrateDropWorkflowTablesSQLite(ctx, raw); err != nil {
			return err
		}
	}

	if _, err := db.ExecContext(ctx,
		`INSERT OR IGNORE INTO schema_migrations (key) VALUES (?)`, "drop_workflow_tables_v1"); err != nil {
		return fmt.Errorf("mark migration: %w", err)
	}
	slog.Info("drop_workflow_tables_v1 migration applied", "driver", store.Driver)
	return nil
}

// dropOrderWorkflow lists tables to drop, children before parents so FK order
// holds even when CASCADE is unavailable (SQLite). gate_jobs / workspace_agent_repos
// are dropped first because they reference harness_runs / workspace_agents.
var dropOrderWorkflow = []string{
	"gate_jobs",                 // FK -> harness_runs
	"workspace_agent_repos",     // FK -> workspace_agents
	"harness_runs",              // FK -> project_templates
	"project_template_columns",  // FK -> project_templates
	"project_templates",         // referenced by issues.default_template_id (FK dropped below)
	"workspace_agents",          // self-FK
	"project_preset_imports",
}

func migrateDropWorkflowTablesSQLite(ctx context.Context, raw *sql.DB) error {
	// Detach FK columns first. SQLite supports ALTER TABLE DROP COLUMN since
	// 3.35 (modernc.org/sqlite embeds a recent enough version). For the
	// harness_run_id / run_id columns we DROP COLUMN then re-add as a plain
	// INTEGER so any dependent index referencing the FK is cleared along with
	// the FK, while keeping the (now FK-less) column for compatibility.
	//
	// Disable FK enforcement during the structural changes.
	if _, err := raw.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable FKs: %w", err)
	}
	defer raw.ExecContext(ctx, `PRAGMA foreign_keys = ON`) //nolint:errcheck

	// 0. Drop the dead workflow indexes BEFORE any DROP COLUMN. SQLite refuses
	//    to drop a column that an index still references, so this must run first.
	if err := dropDeadWorkflowIndexes(ctx, raw); err != nil {
		return err
	}

	// 1. Drop issues.default_template_id (no longer modelled).
	if exists, err := columnExistsWF(raw, "issues", "default_template_id"); err != nil {
		return fmt.Errorf("check issues.default_template_id: %w", err)
	} else if exists {
		if _, err := raw.ExecContext(ctx, `ALTER TABLE issues DROP COLUMN default_template_id`); err != nil {
			return fmt.Errorf("drop issues.default_template_id: %w", err)
		}
	}

	// 2. Re-create the FK-bearing columns as plain INTEGER so the FK is gone.
	//    On SQLite a column added by ALTER TABLE never carries an enforced FK,
	//    and once the referenced table is dropped the residual FK definition (if
	//    any) is harmless with foreign_keys disabled — but to be safe and
	//    self-documenting, rebuild each as DROP+ADD. Indexes on these columns
	//    were already dropped in step 0, so DROP COLUMN won't be blocked.
	fkCols := []struct{ table, col string }{
		{"agent_messages", "harness_run_id"},
		{"workspace_costs", "harness_run_id"},
		{"workspace_tasks", "harness_run_id"},
		{"blackboard_entries", "harness_run_id"},
		{"harness_checks", "run_id"},
	}
	for _, fc := range fkCols {
		exists, err := columnExistsWF(raw, fc.table, fc.col)
		if err != nil {
			return fmt.Errorf("check %s.%s: %w", fc.table, fc.col, err)
		}
		if !exists {
			continue
		}
		if _, err := raw.ExecContext(ctx, "ALTER TABLE "+fc.table+" DROP COLUMN "+fc.col); err != nil {
			return fmt.Errorf("drop %s.%s: %w", fc.table, fc.col, err)
		}
		if _, err := raw.ExecContext(ctx, "ALTER TABLE "+fc.table+" ADD COLUMN "+fc.col+" INTEGER"); err != nil {
			return fmt.Errorf("re-add %s.%s: %w", fc.table, fc.col, err)
		}
	}

	// 3. Drop the workflow tables (children before parents).
	for _, tbl := range dropOrderWorkflow {
		if _, err := raw.ExecContext(ctx, "DROP TABLE IF EXISTS "+tbl); err != nil {
			return fmt.Errorf("drop table %s: %w", tbl, err)
		}
	}
	return nil
}

func migrateDropWorkflowTablesPostgres(ctx context.Context, raw *sql.DB) error {
	// 1. Drop the FK constraints on retained columns. Use DROP CONSTRAINT
	//    IF EXISTS so partial-retry is safe. Constraint names match the ones
	//    declared in schema_postgres.sql's deferred DO $$ block.
	pgDropConstraints := []string{
		`ALTER TABLE agent_messages DROP CONSTRAINT IF EXISTS fk_agent_messages_harness_run_id`,
		`ALTER TABLE workspace_costs DROP CONSTRAINT IF EXISTS fk_workspace_costs_harness_run_id`,
		`ALTER TABLE workspace_tasks DROP CONSTRAINT IF EXISTS fk_workspace_tasks_harness_run_id`,
		`ALTER TABLE issues DROP CONSTRAINT IF EXISTS fk_issues_default_template_id`,
		// blackboard_entries.harness_run_id and harness_checks.run_id FKs were
		// declared inline in CREATE TABLE; PG auto-names them <table>_<col>_fkey.
		`ALTER TABLE blackboard_entries DROP CONSTRAINT IF EXISTS blackboard_entries_harness_run_id_fkey`,
		`ALTER TABLE harness_checks DROP CONSTRAINT IF EXISTS harness_checks_run_id_fkey`,
	}
	for _, stmt := range pgDropConstraints {
		if _, err := raw.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("pg drop constraint (%.50s): %w", stmt, err)
		}
	}

	// 2. Drop issues.default_template_id column.
	if _, err := raw.ExecContext(ctx, `ALTER TABLE issues DROP COLUMN IF EXISTS default_template_id`); err != nil {
		return fmt.Errorf("pg drop issues.default_template_id: %w", err)
	}

	// 2b. Drop the dead workflow indexes. PG would auto-drop them with the
	//     parent table/column, but the harness_run_id / run_id columns are
	//     retained, so drop the indexes explicitly to match the cleaned schema.
	if err := dropDeadWorkflowIndexes(ctx, raw); err != nil {
		return err
	}

	// 3. Drop the workflow tables. CASCADE clears any remaining dependent FK
	//    constraints (e.g. gate_jobs/workspace_agent_repos pointing at parents).
	for _, tbl := range dropOrderWorkflow {
		if _, err := raw.ExecContext(ctx, "DROP TABLE IF EXISTS "+tbl+" CASCADE"); err != nil {
			return fmt.Errorf("pg drop table %s: %w", tbl, err)
		}
	}
	return nil
}
