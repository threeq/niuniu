package migration

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// MigrateDropWorkspacesHarnessID drops the dangling workspaces.harness_id
// column on databases that were migrated through drop_legacy_phase7_v1
// before that migration learned to drop it. Symptom on affected DBs:
// every INSERT into workspaces fails with
//
//	SQL logic error: no such table: main.harnesses (1)
//
// because SQLite's FK validator follows the dangling REFERENCES
// harnesses(id) on the column even though the parent table was dropped
// by Phase 7.
//
// This migration is separate from MigrateDropLegacyPhase7 because that
// migration is gated by schema_migrations.key='drop_legacy_phase7_v1'
// — DBs that already ran the original (buggy) Phase 7 will skip the
// fix unless it lives behind its own marker.
//
// Idempotent: the schema_migrations key
// 'drop_workspaces_harness_id_v1' prevents re-execution.
// Driver-aware: SQLite ALTER TABLE DROP COLUMN works directly;
// PostgreSQL uses DROP COLUMN IF EXISTS.
func MigrateDropWorkspacesHarnessID(ctx context.Context, raw *sql.DB) error {
	db := store.Wrap(raw)

	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		key TEXT PRIMARY KEY,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}
	var dummy string
	err := db.QueryRowContext(ctx, `SELECT key FROM schema_migrations WHERE key = ?`,
		"drop_workspaces_harness_id_v1").Scan(&dummy)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return fmt.Errorf("check migration marker: %w", err)
	}

	// SQLite path: check column existence, then DROP COLUMN.
	// PostgreSQL path: DROP COLUMN IF EXISTS handles missing column.
	if store.Driver == "postgres" {
		if _, err := raw.ExecContext(ctx,
			`ALTER TABLE workspaces DROP COLUMN IF EXISTS harness_id`); err != nil {
			return fmt.Errorf("drop workspaces.harness_id (pg): %w", err)
		}
	} else {
		exists, err := columnExistsP7(raw, "workspaces", "harness_id")
		if err != nil {
			return fmt.Errorf("check workspaces.harness_id: %w", err)
		}
		if exists {
			if _, err := raw.ExecContext(ctx,
				`ALTER TABLE workspaces DROP COLUMN harness_id`); err != nil {
				return fmt.Errorf("drop workspaces.harness_id (sqlite): %w", err)
			}
		}
	}

	if _, err := db.ExecContext(ctx,
		`INSERT OR IGNORE INTO schema_migrations (key) VALUES (?)`,
		"drop_workspaces_harness_id_v1"); err != nil {
		return fmt.Errorf("mark migration: %w", err)
	}

	slog.Info("drop_workspaces_harness_id_v1 migration applied", "driver", store.Driver)
	return nil
}
