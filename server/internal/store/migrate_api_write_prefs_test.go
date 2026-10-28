package store

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// Legacy databases carry external_api_write_prefs keyed by (user_id,
// provider_id). The owner-scope migration must rebuild the table to
// (owner_type, owner_id, provider_id) — mapping every legacy row to a
// ('user', user_id) grant — without losing data, and the rebuilt table must
// accept org-scoped rows.
func TestMigrateAPIWritePrefsOwnerScope(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Seed the LEGACY shape (pre-owner-scope schema.sql) with one grant.
	legacy := `
		CREATE TABLE external_providers (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			name        TEXT NOT NULL,
			label       TEXT NOT NULL DEFAULT '',
			api_base_url TEXT NOT NULL DEFAULT '',
			auth_type   TEXT NOT NULL DEFAULT '',
			auth_header TEXT NOT NULL DEFAULT '',
			auth_prefix TEXT NOT NULL DEFAULT '',
			profile     TEXT NOT NULL DEFAULT '{}',
			openapi_url TEXT NOT NULL DEFAULT '',
			whitelist   TEXT NOT NULL DEFAULT '[]',
			enabled     INTEGER NOT NULL DEFAULT 1,
			created_by  TEXT NOT NULL DEFAULT 'system',
			created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE external_api_write_prefs (
			user_id     INTEGER NOT NULL,
			provider_id INTEGER NOT NULL REFERENCES external_providers(id) ON DELETE CASCADE,
			enabled     INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (user_id, provider_id)
		);
		INSERT INTO external_providers (name) VALUES ('tapd');
		INSERT INTO external_api_write_prefs (user_id, provider_id, enabled) VALUES (7, 1, 1);
	`
	if _, err := db.Exec(legacy); err != nil {
		t.Fatalf("seed legacy schema: %v", err)
	}

	Migrate(db)

	var ownerType string
	var ownerID, enabled int64
	if err := db.QueryRow(
		`SELECT owner_type, owner_id, enabled FROM external_api_write_prefs WHERE provider_id = 1`,
	).Scan(&ownerType, &ownerID, &enabled); err != nil {
		t.Fatalf("legacy row must survive the rebuild: %v", err)
	}
	if ownerType != "user" || ownerID != 7 || enabled != 1 {
		t.Fatalf("migrated row = (%s,%d,%d), want (user,7,1)", ownerType, ownerID, enabled)
	}

	// The rebuilt table must accept org-scoped rows (new PK dimensions).
	if _, err := db.Exec(
		`INSERT INTO external_api_write_prefs (owner_type, owner_id, provider_id, enabled) VALUES ('org', 40, 1, 1)`); err != nil {
		t.Fatalf("org-scoped row must be insertable after migration: %v", err)
	}
	// Re-running Migrate must be a no-op (idempotence).
	Migrate(db)
	var count int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM external_api_write_prefs`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("row count after re-migrate = %d (err=%v), want 2", count, err)
	}
}
