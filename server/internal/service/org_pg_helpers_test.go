package service

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/pgtest"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// openOrgTestDBForDriver returns a *sql.DB seeded with schema + migrations
// for the requested driver. PG branch auto-skips when Docker is unavailable.
// Mirrors openOrgTestDB but parameterized.
func openOrgTestDBForDriver(t *testing.T, drv string) *sql.DB {
	t.Helper()
	switch drv {
	case pgtest.DriverSQLite:
		return openOrgTestDB(t)
	case pgtest.DriverPostgres:
		raw, _ := pgtest.SetupPGDB(t)
		return raw
	default:
		t.Fatalf("openOrgTestDBForDriver: unknown driver %q", drv)
		return nil
	}
}

// newOrgServiceForDriver builds an OrgService backed by the requested driver.
// Uses store.NewQueries (driver-aware) so generated sqlc queries route
// through the PG placeholder rewriter on Postgres.
func newOrgServiceForDriver(t *testing.T, drv string) (*OrgService, *sql.DB, *store.Queries) {
	t.Helper()
	db := openOrgTestDBForDriver(t, drv)
	q := store.NewQueries(db)
	authz := NewAuthz(q, db)
	svc := NewOrgService(q, db, authz)
	return svc, db, q
}

// newOrgTestUserDriver inserts a user via the placeholder-rewriting wrapper,
// so the same `?` placeholders work on both SQLite and Postgres. SQLite path
// is a no-op rewrite; PG path turns `?` into `$N`.
//
// TODO(pg-harness): once every TestOrgService* test has been ported to
// `pgtest.ForEachDriver`, collapse this with the legacy `newOrgTestUser`
// (org_test.go) — the SQLite-only helper exists only to keep unported
// tests working. Same applies to `openOrgTestDBForDriver` vs
// `openOrgTestDB`.
func newOrgTestUserDriver(t *testing.T, db *sql.DB, username, globalRole string) int64 {
	t.Helper()
	var id int64
	if err := store.Wrap(db).QueryRowContext(context.Background(),
		`INSERT INTO users (username, password_hash, display_name, role) VALUES (?, 'x', ?, ?) RETURNING id`,
		username, username, globalRole,
	).Scan(&id); err != nil {
		t.Fatalf("newOrgTestUserDriver: %v", err)
	}
	return id
}
