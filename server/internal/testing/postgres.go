package testing

// PG fixture variants that depend on internal/service. The driver-agnostic
// PG harness primitives (SetupPGDB, ForEachDriver, ...) live in
// internal/pgtest so internal/service tests can import them without an
// import cycle through this package.

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/pgtest"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// SetupPGDB is a re-export of pgtest.SetupPGDB for callers that already
// import this package. Identical signature.
func SetupPGDB(t *testing.T) (*sql.DB, *store.Queries) {
	t.Helper()
	return pgtest.SetupPGDB(t)
}

// NewPGIsolationEnv mirrors NewIsolationEnv but on Postgres. Same scenario:
//
//	OrgA <- UserA
//	OrgB <- UserB
//
// UserA cannot see OrgB's resources, and vice versa.
//
// Auto-skips when Docker is unavailable.
func NewPGIsolationEnv(t *testing.T) *IsolationEnv {
	t.Helper()
	db, _ := pgtest.SetupPGDB(t)

	exec := func(q string, args ...any) sql.Result {
		// Route through the wrapper so `?` placeholders rewrite to `$N`.
		r, err := store.Wrap(db).ExecContext(context.Background(), q, args...)
		if err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
		return r
	}

	env := &IsolationEnv{
		DB:        db,
		OrgOwner:  func(id int64) service.OwnerRef { return service.OwnerRef{Type: "org", ID: id} },
		UserOwner: func(id int64) service.OwnerRef { return service.OwnerRef{Type: "user", ID: id} },
	}

	env.UserA = pgInsertReturningID(t, db, `INSERT INTO users (username, password_hash, role) VALUES ('user-a', 'x', 'admin') RETURNING id`)
	env.UserB = pgInsertReturningID(t, db, `INSERT INTO users (username, password_hash, role) VALUES ('user-b', 'x', 'member') RETURNING id`)

	env.OrgA = pgInsertReturningID(t, db, `INSERT INTO organizations (slug, name, created_by) VALUES ('org-a', 'OrgA', $1) RETURNING id`, env.UserA)
	env.OrgB = pgInsertReturningID(t, db, `INSERT INTO organizations (slug, name, created_by) VALUES ('org-b', 'OrgB', $1) RETURNING id`, env.UserB)

	exec(`INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, 'owner')`, env.OrgA, env.UserA)
	exec(`INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, 'owner')`, env.OrgB, env.UserB)

	return env
}

// pgInsertReturningID runs an INSERT ... RETURNING id and returns the id.
// PG doesn't support LastInsertId(); RETURNING is the canonical replacement.
func pgInsertReturningID(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(query, args...).Scan(&id); err != nil {
		t.Fatalf("pg insert returning id: %v\nSQL: %s", err, query)
	}
	return id
}
