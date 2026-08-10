package store

import (
	"context"
	"fmt"
)

// EnsureMigration applies ddl exactly once, keyed by key in schema_migrations.
// If key is already recorded the function returns nil without running ddl
// (idempotent). If ddl fails the key is NOT recorded so the next startup
// retries automatically after the root cause is fixed.
//
// Use for new migrations going forward. Existing columnExists-based migrations
// in open.go/migrate.go need not be converted — the columnExists guard is still
// idempotent and EnsureMigration is purely additive.
//
// db must be *store.DB (not *sql.DB) so that ? placeholders and
// INSERT OR IGNORE are rewritten correctly for PostgreSQL.
func EnsureMigration(db *DB, key, ddl string) error {
	if migrationApplied(db, key) {
		return nil
	}
	if _, err := db.ExecContext(context.Background(), ddl); err != nil {
		return fmt.Errorf("EnsureMigration %q: %w", key, err)
	}
	markMigration(db, key)
	return nil
}
