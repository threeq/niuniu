package store

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// TestExpandDataSourcesKindCheckSQLite simulates a DB that already ran the
// 'mssql'-era expansion (17 kinds, ending at redis/mongo). The migration must
// rebuild the table so trino/elasticsearch rows insert, preserve existing
// rows and the owner index, and be idempotent on a second run.
func TestExpandDataSourcesKindCheckSQLite(t *testing.T) {
	original := Driver
	defer func() { Driver = original }()
	Driver = "sqlite"

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	// 'mssql'-era schema: 17-kind CHECK without trino/elasticsearch.
	_, err = db.Exec(`
		CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT);
		INSERT INTO users(id) VALUES (1);
		CREATE TABLE data_sources (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			owner_type          TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
			owner_id            INTEGER NOT NULL DEFAULT 0,
			user_id             INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			name                TEXT NOT NULL,
			kind                TEXT NOT NULL CHECK (kind IN (
				'mysql','postgres','clickhouse','mssql',
				'mariadb','tidb','oceanbase','starrocks','doris',
				'cockroachdb','greenplum','redshift','opengauss','polardbpg','yugabyte',
				'redis','mongo'
			)),
			config              TEXT NOT NULL DEFAULT '{}',
			scope_config        TEXT NOT NULL DEFAULT '{}',
			default_access_mode TEXT NOT NULL DEFAULT 'read' CHECK (default_access_mode IN ('read','readwrite')),
			require_confirm     TEXT NOT NULL DEFAULT 'writes_only' CHECK (require_confirm IN ('always','writes_only','never')),
			last_verified_at    TIMESTAMP DEFAULT NULL,
			created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(owner_type, owner_id, name)
		);
		CREATE INDEX idx_data_sources_owner ON data_sources(owner_type, owner_id);
		INSERT INTO data_sources(user_id, name, kind) VALUES (1, 'orders-db', 'mysql');
	`)
	if err != nil {
		t.Fatal(err)
	}

	// Precondition: the 17-kind CHECK rejects trino.
	if _, err := db.Exec(`INSERT INTO data_sources(user_id, name, kind) VALUES (1, 'lake', 'trino')`); err == nil {
		t.Fatal("expected 17-kind CHECK to reject 'trino' before migration")
	}

	expandDataSourcesKindCheckSQLite(db)

	// The new kinds now insert (including the ws-670 http kind).
	for _, kind := range []string{"trino", "elasticsearch", "http"} {
		if _, err := db.Exec(`INSERT INTO data_sources(user_id, name, kind) VALUES (1, 'ds-'||?, ?)`, kind, kind); err != nil {
			t.Fatalf("insert kind %s should succeed after migration: %v", kind, err)
		}
	}

	// Existing row preserved.
	var kind string
	if err := db.QueryRow(`SELECT kind FROM data_sources WHERE name='orders-db'`).Scan(&kind); err != nil || kind != "mysql" {
		t.Fatalf("existing row not preserved: kind=%q err=%v", kind, err)
	}

	// Owner index recreated by the rebuild.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_data_sources_owner'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("idx_data_sources_owner not recreated (n=%d err=%v)", n, err)
	}

	// Idempotent: second run is a no-op and loses no rows.
	expandDataSourcesKindCheckSQLite(db)
	if err := db.QueryRow(`SELECT COUNT(*) FROM data_sources`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("rows lost after second run (n=%d err=%v)", n, err)
	}
}

// TestExpandDataSourcesKindCheckSQLiteTrinoEra verifies a DB at the 'trino'-era
// expansion (has trino/elasticsearch but not http) is re-migrated to accept the
// http kind — the idempotency marker moved from 'trino' to 'http'.
func TestExpandDataSourcesKindCheckSQLiteTrinoEra(t *testing.T) {
	original := Driver
	defer func() { Driver = original }()
	Driver = "sqlite"

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	_, err = db.Exec(`
		CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT);
		INSERT INTO users(id) VALUES (1);
		CREATE TABLE data_sources (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			owner_type          TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
			owner_id            INTEGER NOT NULL DEFAULT 0,
			user_id             INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			name                TEXT NOT NULL,
			kind                TEXT NOT NULL CHECK (kind IN (
				'mysql','postgres','clickhouse','mssql',
				'mariadb','tidb','oceanbase','starrocks','doris',
				'cockroachdb','greenplum','redshift','opengauss','polardbpg','yugabyte',
				'redis','mongo','trino','elasticsearch'
			)),
			config              TEXT NOT NULL DEFAULT '{}',
			scope_config        TEXT NOT NULL DEFAULT '{}',
			default_access_mode TEXT NOT NULL DEFAULT 'read' CHECK (default_access_mode IN ('read','readwrite')),
			require_confirm     TEXT NOT NULL DEFAULT 'writes_only' CHECK (require_confirm IN ('always','writes_only','never')),
			last_verified_at    TIMESTAMP DEFAULT NULL,
			created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(owner_type, owner_id, name)
		);
		CREATE INDEX idx_data_sources_owner ON data_sources(owner_type, owner_id);
		INSERT INTO data_sources(user_id, name, kind) VALUES (1, 'lake', 'trino');
	`)
	if err != nil {
		t.Fatal(err)
	}

	// Precondition: the trino-era CHECK rejects http.
	if _, err := db.Exec(`INSERT INTO data_sources(user_id, name, kind) VALUES (1, 'api', 'http')`); err == nil {
		t.Fatal("expected trino-era CHECK to reject 'http' before migration")
	}

	expandDataSourcesKindCheckSQLite(db)

	if _, err := db.Exec(`INSERT INTO data_sources(user_id, name, kind) VALUES (1, 'api', 'http')`); err != nil {
		t.Fatalf("http insert should succeed after re-migration: %v", err)
	}
}
