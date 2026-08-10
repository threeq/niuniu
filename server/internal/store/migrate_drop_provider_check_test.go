package store

import (
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// TestDropProviderCheck_FreshSchema verifies that on a freshly-applied schema
// (which no longer carries the enum CHECK), a custom provider name like
// 'tapd_personal' can be inserted into external_provider_credentials and
// project_external_sources without tripping a CHECK constraint.
//
// :memory: opened without a foreign_keys pragma has FK enforcement OFF, so we
// don't need to seed users/projects parent rows.
func TestDropProviderCheck_FreshSchema(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	Driver = "sqlite"

	if err := ApplySchema(db); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	Migrate(db)
	// Keep FK off so we can insert without seeding users/projects parent rows.
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("disable FK: %v", err)
	}

	if _, err := db.Exec(
		`INSERT INTO external_provider_credentials
			(owner_type, owner_id, user_id, provider, alias, config)
		 VALUES ('user', 1, 1, 'tapd_personal', '', '{}')`,
	); err != nil {
		t.Fatalf("insert custom provider into external_provider_credentials failed: %v", err)
	}

	if _, err := db.Exec(
		`INSERT INTO project_external_sources
			(project_id, provider, source_key, config)
		 VALUES (1, 'tapd_personal', 'WS-1', '{}')`,
	); err != nil {
		t.Fatalf("insert custom provider into project_external_sources failed: %v", err)
	}
}

// TestDropProviderCheck_ExistingDB simulates an EXISTING database created with
// the old hardcoded CHECK (provider IN ('github','jira','tapd')) constraint,
// runs the migration, and asserts that a custom provider name then inserts
// successfully -- proving the migration (not just the fresh schema) removes the
// CHECK and preserves the table shape.
func TestDropProviderCheck_ExistingDB(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	Driver = "sqlite"

	// Recreate the OLD table definitions verbatim, including the enum CHECK.
	oldDDL := []string{
		`CREATE TABLE external_provider_credentials (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			owner_type       TEXT NOT NULL CHECK (owner_type IN ('user','org')),
			owner_id         INTEGER NOT NULL,
			user_id          INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			provider         TEXT NOT NULL CHECK (provider IN ('github','jira','tapd')),
			alias            TEXT NOT NULL DEFAULT '',
			config           TEXT NOT NULL DEFAULT '{}',
			last_verified_at TIMESTAMP DEFAULT NULL,
			created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(owner_type, owner_id, user_id, provider, alias)
		)`,
		`CREATE TABLE project_external_sources (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id    INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			provider      TEXT NOT NULL CHECK (provider IN ('github','jira','tapd')),
			source_key    TEXT NOT NULL,
			credential_id INTEGER REFERENCES external_provider_credentials(id) ON DELETE RESTRICT,
			config        TEXT NOT NULL DEFAULT '{}',
			created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(project_id, provider, source_key)
		)`,
		`CREATE TABLE external_user_identities (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			provider      TEXT NOT NULL CHECK (provider IN ('github','jira','tapd')),
			external_user TEXT NOT NULL,
			display_name  TEXT NOT NULL DEFAULT '',
			avatar_url    TEXT NOT NULL DEFAULT '',
			created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(user_id, provider, external_user)
		)`,
		`CREATE TABLE external_write_prefs (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			provider    TEXT NOT NULL CHECK (provider IN ('github','jira','tapd')),
			enabled     INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0,1)),
			created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(user_id, provider)
		)`,
	}
	for _, ddl := range oldDDL {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("create old table: %v", err)
		}
	}

	// Pre-existing rows with a built-in provider should survive the rebuild.
	if _, err := db.Exec(
		`INSERT INTO external_provider_credentials
			(owner_type, owner_id, user_id, provider, alias, config)
		 VALUES ('user', 1, 1, 'github', 'gh1', '{"a":1}')`,
	); err != nil {
		t.Fatalf("seed legacy github row: %v", err)
	}

	// Sanity: the old CHECK actually rejects a custom provider before migration.
	if _, err := db.Exec(
		`INSERT INTO external_provider_credentials
			(owner_type, owner_id, user_id, provider, alias, config)
		 VALUES ('user', 1, 1, 'tapd_personal', '', '{}')`,
	); err == nil {
		t.Fatal("expected old CHECK constraint to reject 'tapd_personal' before migration")
	} else if !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("expected CHECK constraint failure, got: %v", err)
	}

	// Run the migration that drops the provider CHECK. The SQLite path
	// re-enables PRAGMA foreign_keys at the end (matching production), so turn
	// it back off here -- this test exercises the CHECK constraint, not FKs,
	// and the parent users/projects tables aren't present in this minimal DB.
	migrateExternalProviderDropProviderCheck(db)
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("disable FK after migration: %v", err)
	}

	// The seeded legacy row must still be present and intact.
	var provider, alias, config string
	if err := db.QueryRow(
		`SELECT provider, alias, config FROM external_provider_credentials WHERE id = 1`,
	).Scan(&provider, &alias, &config); err != nil {
		t.Fatalf("legacy row lost after migration: %v", err)
	}
	if provider != "github" || alias != "gh1" || config != `{"a":1}` {
		t.Fatalf("legacy row corrupted: provider=%q alias=%q config=%q", provider, alias, config)
	}

	// Now a custom provider name must insert successfully into both tables.
	if _, err := db.Exec(
		`INSERT INTO external_provider_credentials
			(owner_type, owner_id, user_id, provider, alias, config)
		 VALUES ('user', 1, 1, 'tapd_personal', '', '{}')`,
	); err != nil {
		t.Fatalf("post-migration insert custom provider (credentials) failed: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO project_external_sources
			(project_id, provider, source_key, config)
		 VALUES (1, 'tapd_personal', 'WS-1', '{}')`,
	); err != nil {
		t.Fatalf("post-migration insert custom provider (sources) failed: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO external_user_identities
			(user_id, provider, external_user)
		 VALUES (1, 'tapd_personal', 'alice')`,
	); err != nil {
		t.Fatalf("post-migration insert custom provider (identities) failed: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO external_write_prefs
			(user_id, provider, enabled)
		 VALUES (1, 'tapd_personal', 1)`,
	); err != nil {
		t.Fatalf("post-migration insert custom provider (write_prefs) failed: %v", err)
	}

	// Indexes must have been recreated.
	for _, idx := range []string{"idx_external_creds_owner", "idx_project_external_sources_project", "idx_external_user_identities_provider_user"} {
		var n int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx,
		).Scan(&n); err != nil || n != 1 {
			t.Errorf("index %s missing after migration (n=%d, err=%v)", idx, n, err)
		}
	}

	// Idempotency: a second run must be a no-op and leave inserts working.
	migrateExternalProviderDropProviderCheck(db)
	if _, err := db.Exec(
		`INSERT INTO external_provider_credentials
			(owner_type, owner_id, user_id, provider, alias, config)
		 VALUES ('user', 1, 2, 'jira_cloud', '', '{}')`,
	); err != nil {
		t.Fatalf("insert after second migration run failed: %v", err)
	}
}
