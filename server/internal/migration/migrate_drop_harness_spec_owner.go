package migration

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// MigrateDropHarnessSpecOwner physically removes the legacy owner/scope/project
// columns from harness_specs, converging it to a single GLOBAL engineering-
// standards library (the per-kanban relationship lives in column_gate_specs).
//
// Dropped columns: scope, project_id, owner_type, owner_id. The old
// UNIQUE(scope, project_id, category, name) and the scope/owner partial indexes
// go away with those columns; a plain UNIQUE(category, name) replaces them.
//
// Idempotent: guarded by the schema_migrations key 'harness_specs_drop_owner_v1'
// AND by the presence of the `scope` column (fresh installs from the new
// schema.sql never had it, so this is a no-op there). Driver-aware.
func MigrateDropHarnessSpecOwner(ctx context.Context, raw *sql.DB) error {
	db := store.Wrap(raw)

	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		key TEXT PRIMARY KEY,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}
	var dummy string
	err := db.QueryRowContext(ctx, `SELECT key FROM schema_migrations WHERE key = ?`,
		"harness_specs_drop_owner_v1").Scan(&dummy)
	if err == nil {
		return nil // already applied
	}
	if err != sql.ErrNoRows {
		return fmt.Errorf("check migration marker: %w", err)
	}

	// Guard: if the legacy `scope` column is absent, the table is already in the
	// new shape (fresh install) — just record the marker and return.
	hasScope, err := harnessSpecsHasColumn(ctx, raw, "scope")
	if err != nil {
		return fmt.Errorf("check harness_specs.scope: %w", err)
	}
	if !hasScope {
		return markHarnessSpecOwnerDone(ctx, db)
	}

	// Dedup by (category, name) — the loose old UNIQUE (scope, project_id, ...)
	// with project_id NULL allowed duplicates on some installs; keep the lowest id.
	if store.Driver == "postgres" {
		if _, err := raw.ExecContext(ctx,
			`DELETE FROM harness_specs a USING harness_specs b
			 WHERE a.category = b.category AND a.name = b.name AND a.id > b.id`); err != nil {
			return fmt.Errorf("dedup harness_specs (pg): %w", err)
		}
		if err := dropHarnessSpecOwnerPostgres(ctx, raw); err != nil {
			return err
		}
	} else {
		if _, err := raw.ExecContext(ctx,
			`DELETE FROM harness_specs WHERE id NOT IN (
			   SELECT MIN(id) FROM harness_specs GROUP BY category, name)`); err != nil {
			return fmt.Errorf("dedup harness_specs (sqlite): %w", err)
		}
		if err := dropHarnessSpecOwnerSQLite(ctx, raw); err != nil {
			return err
		}
	}

	if err := markHarnessSpecOwnerDone(ctx, db); err != nil {
		return err
	}
	slog.Info("harness_specs_drop_owner_v1 migration applied", "driver", store.Driver)
	return nil
}

func markHarnessSpecOwnerDone(ctx context.Context, db *store.DB) error {
	if _, err := db.ExecContext(ctx,
		`INSERT OR IGNORE INTO schema_migrations (key) VALUES (?)`, "harness_specs_drop_owner_v1"); err != nil {
		return fmt.Errorf("mark migration: %w", err)
	}
	return nil
}

// harnessSpecsHasColumn reports whether harness_specs has the named column.
func harnessSpecsHasColumn(ctx context.Context, db *sql.DB, col string) (bool, error) {
	var n int
	var err error
	if store.Driver == "postgres" {
		err = db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'harness_specs' AND column_name = $1`,
			col).Scan(&n)
	} else {
		err = db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM pragma_table_info('harness_specs') WHERE name = ?`, col).Scan(&n)
	}
	return n > 0, err
}

// dropHarnessSpecOwnerPostgres drops the 4 legacy columns (CASCADE removes the
// dependent UNIQUE constraint, FK, and partial indexes) then adds the new
// UNIQUE(category, name).
func dropHarnessSpecOwnerPostgres(ctx context.Context, db *sql.DB) error {
	for _, col := range []string{"scope", "project_id", "owner_type", "owner_id"} {
		if _, err := db.ExecContext(ctx,
			`ALTER TABLE harness_specs DROP COLUMN IF EXISTS `+col+` CASCADE`); err != nil {
			return fmt.Errorf("pg drop harness_specs.%s: %w", col, err)
		}
	}
	// Idempotent add of the global unique constraint.
	if _, err := db.ExecContext(ctx, `DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'harness_specs_category_name_key') THEN
			ALTER TABLE harness_specs ADD CONSTRAINT harness_specs_category_name_key UNIQUE (category, name);
		END IF;
	END $$;`); err != nil {
		return fmt.Errorf("pg add harness_specs unique(category,name): %w", err)
	}
	return nil
}

// dropHarnessSpecOwnerSQLite rebuilds harness_specs without the legacy columns
// (SQLite cannot drop a column that participates in a table-level UNIQUE).
// Pattern mirrors rebuildWorkspaceAgentsSQLite: FK off -> CREATE new -> copy ->
// DROP old -> RENAME -> recreate indexes. IDs are preserved so column_gate_specs
// / harness_checks foreign keys stay valid.
func dropHarnessSpecOwnerSQLite(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable FKs: %w", err)
	}
	defer db.ExecContext(ctx, `PRAGMA foreign_keys = ON`) //nolint:errcheck

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	stmts := []string{
		`CREATE TABLE harness_specs_new (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			category    TEXT NOT NULL CHECK (category IN ('commit', 'quality', 'workflow', 'agent')),
			name        TEXT NOT NULL,
			enabled     INTEGER NOT NULL DEFAULT 1,
			severity    TEXT NOT NULL DEFAULT 'warning' CHECK (severity IN ('error', 'warning', 'info')),
			config      TEXT NOT NULL DEFAULT '{}',
			kind                TEXT    NOT NULL DEFAULT 'regex_match',
			target              TEXT    NOT NULL DEFAULT '',
			pattern             TEXT    NOT NULL DEFAULT '',
			pattern_flags       TEXT    NOT NULL DEFAULT '',
			command             TEXT    NOT NULL DEFAULT '',
			timeout_sec         INTEGER NOT NULL DEFAULT 120,
			expected_exit_code  INTEGER NOT NULL DEFAULT 0,
			extract_regex       TEXT    NOT NULL DEFAULT '',
			threshold_value     REAL    NOT NULL DEFAULT 0,
			threshold_op        TEXT    NOT NULL DEFAULT '',
			file_paths          TEXT    NOT NULL DEFAULT '[]',
			trigger_on          TEXT    NOT NULL DEFAULT 'phase_exit',
			judge_prompt        TEXT    NOT NULL DEFAULT '',
			judge_model         TEXT    NOT NULL DEFAULT 'claude-haiku-4-5-20251001',
			created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(category, name)
		)`,
		`INSERT INTO harness_specs_new
			SELECT id, category, name, enabled, severity, config, kind, target, pattern,
			       pattern_flags, command, timeout_sec, expected_exit_code, extract_regex,
			       threshold_value, threshold_op, file_paths, trigger_on, judge_prompt,
			       judge_model, created_at, updated_at
			FROM harness_specs`,
		`DROP TABLE harness_specs`,
		`ALTER TABLE harness_specs_new RENAME TO harness_specs`,
		`CREATE INDEX IF NOT EXISTS idx_harness_specs_category ON harness_specs(category)`,
		`CREATE INDEX IF NOT EXISTS idx_harness_specs_kind ON harness_specs(kind)`,
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			_ = tx.Rollback()
			preview := stmt
			if len(preview) > 60 {
				preview = preview[:60]
			}
			return fmt.Errorf("rebuild harness_specs step %q: %w", preview, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit harness_specs rebuild: %w", err)
	}
	return nil
}
