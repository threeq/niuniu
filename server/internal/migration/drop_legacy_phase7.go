package migration

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// columnExistsP7 checks whether a column exists in a table (SQLite only).
// Used internally by the phase7 migration to guard DROP COLUMN statements.
func columnExistsP7(db *sql.DB, table, col string) (bool, error) {
	var count int
	err := db.QueryRow(
		"SELECT COUNT(*) FROM pragma_table_info('"+table+"') WHERE name = ?", col,
	).Scan(&count)
	return count > 0, err
}

// tableExistsP7 reports whether a table exists. Driver-aware. Used to guard
// phase7 steps that touch workspace_agents — which no longer exists on fresh
// installs from the post-workflow-decommission schema (the
// MigrateDropWorkflowTables migration drops it, and new schema.sql never
// creates it).
func tableExistsP7(db *sql.DB, table string) (bool, error) {
	var count int
	var err error
	if store.Driver == "postgres" {
		err = db.QueryRow(
			"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name=$1",
			table,
		).Scan(&count)
	} else {
		err = db.QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&count)
	}
	return count > 0, err
}

// MigrateDropLegacyPhase7 drops the six legacy tables
// (harness_phases, harness_phase_agents, harness_phase_gates,
// teams, team_members, harnesses), removes legacy columns from
// harness_runs / blackboard_entries / workspaces, and converges the
// workspace_agents.source CHECK constraint to ('run','subagent','manual').
//
// Idempotent: the schema_migrations key 'drop_legacy_phase7_v1' prevents
// re-execution. Driver-aware: uses different DDL for SQLite vs PostgreSQL.
func MigrateDropLegacyPhase7(ctx context.Context, raw *sql.DB) error {
	db := store.Wrap(raw)

	// Ensure schema_migrations table exists and check marker.
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		key TEXT PRIMARY KEY,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}
	var dummy string
	err := db.QueryRowContext(ctx, `SELECT key FROM schema_migrations WHERE key = ?`,
		"drop_legacy_phase7_v1").Scan(&dummy)
	if err == nil {
		// Already applied.
		return nil
	}
	if err != sql.ErrNoRows {
		return fmt.Errorf("check migration marker: %w", err)
	}

	if store.Driver == "postgres" {
		return migrateDropLegacyPhase7Postgres(ctx, db, raw)
	}
	return migrateDropLegacyPhase7SQLite(ctx, db, raw)
}

// migrateDropLegacyPhase7SQLite performs the drop migration on SQLite.
// SQLite supports ALTER TABLE DROP COLUMN since 3.35 (2021); modernc.org/sqlite
// embeds a recent enough version.
func migrateDropLegacyPhase7SQLite(ctx context.Context, db *store.DB, raw *sql.DB) error {
	// Step 1: Mark running legacy harness_id-driven runs as failed.
	// Guard: if harness_id column doesn't exist (fresh install from new schema.sql),
	// this step is a no-op.
	if hasHarnessID, _ := columnExistsP7(raw, "harness_runs", "harness_id"); hasHarnessID {
		if _, err := db.ExecContext(ctx, `UPDATE harness_runs
			SET status = 'failed',
			    error_message = 'legacy harness_id-driven run; not migrated to column model'
			WHERE harness_id IS NOT NULL
			  AND status NOT IN ('completed','aborted','cancelled','failed','stopped_max_rounds')`); err != nil {
			return fmt.Errorf("mark legacy runs failed: %w", err)
		}
	}

	// Step 2: Converge workspace_agents.source values before changing CHECK.
	// Guard on table existence: post-workflow-decommission schema no longer has
	// workspace_agents (dropped by MigrateDropWorkflowTables / never created on
	// fresh installs).
	if waExists, err := tableExistsP7(raw, "workspace_agents"); err != nil {
		return fmt.Errorf("check workspace_agents existence: %w", err)
	} else if waExists {
		if _, err := db.ExecContext(ctx, `UPDATE workspace_agents
			SET source = 'manual'
			WHERE source IN ('team','harness_phase')`); err != nil {
			return fmt.Errorf("update workspace_agents source: %w", err)
		}
	}

	// Step 3: Drop FK columns before dropping referenced tables.
	// SQLite supports DROP COLUMN in recent versions.
	colDrops := []struct{ table, col string }{
		{"harness_runs", "harness_id"},
		{"harness_runs", "current_phase_id"},
		{"harness_runs", "current_phase_name"},
		{"blackboard_entries", "phase_id"},
		{"workspaces", "team_id"},
		{"workspaces", "harness_id"},
	}
	for _, cd := range colDrops {
		exists, err := columnExistsP7(raw, cd.table, cd.col)
		if err != nil {
			return fmt.Errorf("check column %s.%s: %w", cd.table, cd.col, err)
		}
		if !exists {
			continue
		}
		if _, err := raw.ExecContext(ctx, "ALTER TABLE "+cd.table+" DROP COLUMN "+cd.col); err != nil {
			return fmt.Errorf("drop column %s.%s: %w", cd.table, cd.col, err)
		}
	}

	// Step 4: Rebuild workspace_agents to update the source CHECK constraint.
	// SQLite requires a table rebuild to change CHECK constraints.
	if err := rebuildWorkspaceAgentsSQLite(ctx, raw); err != nil {
		return fmt.Errorf("rebuild workspace_agents: %w", err)
	}

	// Step 5: Drop legacy tables (child before parent, FK order).
	dropOrder := []string{
		"harness_phase_gates",
		"harness_phase_agents",
		"harness_phases",
		"team_members",
		"teams",
		"harnesses",
	}
	for _, tbl := range dropOrder {
		if _, err := raw.ExecContext(ctx, "DROP TABLE IF EXISTS "+tbl); err != nil {
			return fmt.Errorf("drop table %s: %w", tbl, err)
		}
	}

	// Step 6: Mark migration as done.
	if _, err := db.ExecContext(ctx,
		`INSERT OR IGNORE INTO schema_migrations (key) VALUES (?)`, "drop_legacy_phase7_v1"); err != nil {
		return fmt.Errorf("mark migration: %w", err)
	}

	slog.Info("drop_legacy_phase7_v1 migration applied (SQLite)")
	return nil
}

// rebuildWorkspaceAgentsSQLite rebuilds the workspace_agents table with the
// updated source CHECK constraint ('run','subagent','manual').
// Pattern: CREATE new → INSERT SELECT → DROP old → RENAME new.
func rebuildWorkspaceAgentsSQLite(ctx context.Context, db *sql.DB) error {
	// Check if rebuild is needed by inspecting current DDL.
	var ddl string
	if err := db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='workspace_agents'`).Scan(&ddl); err != nil {
		if err == sql.ErrNoRows {
			return nil // table gone already
		}
		return fmt.Errorf("read workspace_agents DDL: %w", err)
	}
	// If the CHECK already has 'run' but not 'team'/'harness_phase', skip.
	if strings.Contains(ddl, "'run'") && !strings.Contains(ddl, "'team'") {
		return nil
	}

	// Disable FK enforcement during rebuild (SQLite requirement).
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable FKs: %w", err)
	}
	defer db.ExecContext(ctx, `PRAGMA foreign_keys = ON`) //nolint:errcheck

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	stmts := []string{
		`CREATE TABLE workspace_agents_new (
			id                    INTEGER PRIMARY KEY AUTOINCREMENT,
			workspace_id          INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
			agent_id              INTEGER REFERENCES agents(id),
			role                  TEXT NOT NULL DEFAULT 'worker' CHECK(role IN ('coordinator', 'worker')),
			capabilities          TEXT NOT NULL DEFAULT '',
			source                TEXT NOT NULL CHECK(source IN ('run', 'subagent', 'manual')),
			source_ref_id         INTEGER,
			driver                TEXT NOT NULL DEFAULT 'claude-cli',
			session_id            TEXT,
			execution_mode        TEXT NOT NULL DEFAULT 'sequential' CHECK(execution_mode IN ('sequential', 'parallel')),
			status                TEXT NOT NULL DEFAULT 'idle' CHECK(status IN ('idle', 'active', 'error', 'exited')),
			parent_agent_id       INTEGER REFERENCES workspace_agents(id) ON DELETE SET NULL,
			subagent_type         TEXT,
			dispatch_tool_use_id  TEXT,
			last_error            TEXT,
			joined_at             TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			exited_at             TIMESTAMP
		)`,
		`INSERT INTO workspace_agents_new
			SELECT id, workspace_id, agent_id, role, capabilities, source, source_ref_id,
			       driver, session_id, execution_mode, status,
			       parent_agent_id, subagent_type, dispatch_tool_use_id, last_error,
			       joined_at, exited_at
			FROM workspace_agents`,
		`DROP TABLE workspace_agents`,
		`ALTER TABLE workspace_agents_new RENAME TO workspace_agents`,
		`CREATE INDEX IF NOT EXISTS idx_workspace_agents_workspace ON workspace_agents(workspace_id)`,
		`CREATE INDEX IF NOT EXISTS idx_workspace_agents_status ON workspace_agents(workspace_id, status)`,
		`CREATE INDEX IF NOT EXISTS idx_workspace_agents_parent ON workspace_agents(workspace_id, parent_agent_id)`,
	}

	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			_ = tx.Rollback()
			preview := stmt
			if len(preview) > 60 {
				preview = preview[:60]
			}
			return fmt.Errorf("rebuild workspace_agents step %q: %w", preview, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit workspace_agents rebuild: %w", err)
	}
	return nil
}

// migrateDropLegacyPhase7Postgres performs the drop migration on PostgreSQL.
func migrateDropLegacyPhase7Postgres(ctx context.Context, db *store.DB, raw *sql.DB) error {
	// Step 1: Mark running legacy harness_id-driven runs as failed.
	// Guard with column existence — partial-retry safety: column may already
	// be dropped by a prior failed migration attempt.
	var hasHarnessID int
	if err := raw.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'harness_runs'
		  AND column_name = 'harness_id'`).Scan(&hasHarnessID); err != nil {
		return fmt.Errorf("check harness_id column (pg): %w", err)
	}
	if hasHarnessID > 0 {
		if _, err := db.ExecContext(ctx, `UPDATE harness_runs
			SET status = 'failed',
			    error_message = 'legacy harness_id-driven run; not migrated to column model'
			WHERE harness_id IS NOT NULL
			  AND status NOT IN ('completed','aborted','cancelled','failed','stopped_max_rounds')`); err != nil {
			return fmt.Errorf("mark legacy runs failed (pg): %w", err)
		}
	}

	// Step 2: Converge workspace_agents.source values. Guard on table existence:
	// post-workflow-decommission schema no longer has workspace_agents.
	waExists, err := tableExistsP7(raw, "workspace_agents")
	if err != nil {
		return fmt.Errorf("check workspace_agents existence (pg): %w", err)
	}
	if waExists {
		if _, err := db.ExecContext(ctx, `UPDATE workspace_agents
			SET source = 'manual'
			WHERE source IN ('team','harness_phase')`); err != nil {
			return fmt.Errorf("update workspace_agents source (pg): %w", err)
		}
	}

	// Step 3: Drop FK columns (PG supports DROP COLUMN IF EXISTS).
	pgColDrops := []string{
		`ALTER TABLE harness_runs DROP COLUMN IF EXISTS harness_id`,
		`ALTER TABLE harness_runs DROP COLUMN IF EXISTS current_phase_id`,
		`ALTER TABLE harness_runs DROP COLUMN IF EXISTS current_phase_name`,
		`ALTER TABLE blackboard_entries DROP COLUMN IF EXISTS phase_id`,
		`ALTER TABLE workspaces DROP COLUMN IF EXISTS team_id`,
		`ALTER TABLE workspaces DROP COLUMN IF EXISTS harness_id`,
	}
	for _, stmt := range pgColDrops {
		if _, err := raw.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("pg drop column (%s): %w", stmt[:40], err)
		}
	}

	// Step 4: Update workspace_agents source CHECK constraint (only if the table
	// still exists).
	if waExists {
		// Find existing constraint name for source column.
		var constraintName string
		cerr := raw.QueryRowContext(ctx, `
			SELECT cc.constraint_name
			FROM information_schema.check_constraints cc
			JOIN information_schema.constraint_column_usage ccu
			  ON cc.constraint_name = ccu.constraint_name
			WHERE ccu.table_schema = 'public'
			  AND ccu.table_name = 'workspace_agents'
			  AND ccu.column_name = 'source'
			LIMIT 1`).Scan(&constraintName)
		if cerr != nil && cerr != sql.ErrNoRows {
			return fmt.Errorf("find source CHECK constraint (pg): %w", cerr)
		}
		if constraintName != "" {
			if _, err := raw.ExecContext(ctx,
				`ALTER TABLE workspace_agents DROP CONSTRAINT IF EXISTS `+constraintName); err != nil {
				return fmt.Errorf("drop source CHECK constraint (pg): %w", err)
			}
		}
		// Add new CHECK constraint.
		if _, err := raw.ExecContext(ctx,
			`ALTER TABLE workspace_agents ADD CONSTRAINT workspace_agents_source_check
			 CHECK (source IN ('run','subagent','manual'))`); err != nil {
			return fmt.Errorf("add new source CHECK constraint (pg): %w", err)
		}
	}

	// Step 5: Drop legacy tables (child before parent).
	dropOrder := []string{
		"harness_phase_gates",
		"harness_phase_agents",
		"harness_phases",
		"team_members",
		"teams",
		"harnesses",
	}
	for _, tbl := range dropOrder {
		// CASCADE drops dependent FK constraints/views without manual cleanup.
		if _, err := raw.ExecContext(ctx, "DROP TABLE IF EXISTS "+tbl+" CASCADE"); err != nil {
			return fmt.Errorf("drop table %s (pg): %w", tbl, err)
		}
	}

	// Step 6: Mark migration as done.
	if _, err := db.ExecContext(ctx,
		`INSERT OR IGNORE INTO schema_migrations (key) VALUES (?)`, "drop_legacy_phase7_v1"); err != nil {
		return fmt.Errorf("mark migration (pg): %w", err)
	}

	slog.Info("drop_legacy_phase7_v1 migration applied (PostgreSQL)")
	return nil
}
