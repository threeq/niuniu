package store_test

import (
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/niuniu-dev/niuniu/internal/pgtest"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// TestExpandDataSourcesKindCheckPostgres is the PG twin of the SQLite
// migration test: an 'mssql'-era 17-kind CHECK must be re-expanded so
// trino/elasticsearch rows insert, existing rows survive, and a second run is
// a no-op. Auto-skips when Docker (or NIUNIU_TEST_PG_DSN) is unavailable.
func TestExpandDataSourcesKindCheckPostgres(t *testing.T) {
	dsn := pgtest.NewSchemaDSN(t)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// 'mssql'-era PG schema: 17-kind CHECK without trino/elasticsearch.
	if _, err := db.Exec(`
		CREATE TABLE users (id BIGSERIAL PRIMARY KEY);
		INSERT INTO users(id) VALUES (1);
		CREATE TABLE data_sources (
			id                  BIGSERIAL PRIMARY KEY,
			owner_type          TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
			owner_id            BIGINT NOT NULL DEFAULT 0,
			user_id             BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
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
		INSERT INTO data_sources(user_id, name, kind) VALUES (1, 'legacy-mysql', 'mysql');
	`); err != nil {
		t.Fatalf("seed legacy schema: %v", err)
	}

	// Pre-condition: trino is rejected by the old CHECK.
	if _, err := db.Exec(`INSERT INTO data_sources(user_id, name, kind) VALUES (1, 'lake', 'trino')`); err == nil {
		t.Fatal("legacy CHECK should reject trino")
	}

	insertNewKinds := func(t *testing.T, suffix string) {
		t.Helper()
		for _, kind := range []string{"trino", "elasticsearch"} {
			if _, err := db.Exec(
				`INSERT INTO data_sources(user_id, name, kind) VALUES (1, $1, $2)`,
				kind+suffix, kind,
			); err != nil {
				t.Fatalf("insert %s after migration: %v", kind, err)
			}
		}
	}

	store.ExpandDataSourcesKindCheckPostgresForTest(db)
	insertNewKinds(t, "-1")

	// Existing rows survive the constraint swap.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM data_sources WHERE name = 'legacy-mysql'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("legacy row lost: n=%d err=%v", n, err)
	}

	// Idempotent: second run keeps the constraint working.
	store.ExpandDataSourcesKindCheckPostgresForTest(db)
	insertNewKinds(t, "-2")

	// The CHECK is still enforced (not silently dropped).
	if _, err := db.Exec(`INSERT INTO data_sources(user_id, name, kind) VALUES (1, 'bogus', 'not-a-kind')`); err == nil {
		t.Fatal("kind CHECK should still reject unknown kinds after migration")
	}
}
