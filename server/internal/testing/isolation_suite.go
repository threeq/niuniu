package testing

import (
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/pgtest"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	_ "modernc.org/sqlite"
)

// IsolationEnv holds two orgs and two users with disjoint memberships so
// isolation tests have a ready-made scenario:
//
//	OrgA <- UserA
//	OrgB <- UserB
//
// UserA cannot see OrgB's resources, and vice versa.
type IsolationEnv struct {
	DB        *sql.DB
	UserA     int64
	UserB     int64
	OrgA      int64
	OrgB      int64
	OrgOwner  func(orgID int64) service.OwnerRef
	UserOwner func(userID int64) service.OwnerRef
}

func NewIsolationEnv(t *testing.T) *IsolationEnv {
	t.Helper()
	// Reset store.Driver to sqlite so a prior dual-driver subtest that left
	// Driver in "postgres" state doesn't make Migrate take the PG branch on
	// this SQLite DB. See SetupPGDB doc for the side-effect rationale.
	store.Driver = pgtest.DriverSQLite
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Use ApplySchema (not bare db.Exec(store.Schema)) so the per-table
	// column-add migrations run alongside the embedded schema — mirrors
	// what production Open() does. Without this, fixtures that depend on
	// columns added in open.go (e.g. issues.external_source from M0.3)
	// would miss them and trigger CREATE INDEX failures in store.Migrate().
	if err := store.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	store.Migrate(db)

	exec := func(q string, args ...any) sql.Result {
		r, err := db.Exec(q, args...)
		if err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
		return r
	}
	lastInsertID := func(r sql.Result) int64 {
		id, err := r.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	env := &IsolationEnv{
		DB:        db,
		OrgOwner:  func(id int64) service.OwnerRef { return service.OwnerRef{Type: "org", ID: id} },
		UserOwner: func(id int64) service.OwnerRef { return service.OwnerRef{Type: "user", ID: id} },
	}

	env.UserA = lastInsertID(exec(`INSERT INTO users (username, password_hash, role) VALUES ('user-a', 'x', 'admin')`))
	env.UserB = lastInsertID(exec(`INSERT INTO users (username, password_hash, role) VALUES ('user-b', 'x', 'member')`))

	env.OrgA = lastInsertID(exec(`INSERT INTO organizations (slug, name, created_by) VALUES ('org-a', 'OrgA', ?)`, env.UserA))
	env.OrgB = lastInsertID(exec(`INSERT INTO organizations (slug, name, created_by) VALUES ('org-b', 'OrgB', ?)`, env.UserB))

	exec(`INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, 'owner')`, env.OrgA, env.UserA)
	exec(`INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, 'owner')`, env.OrgB, env.UserB)

	return env
}

// AssertInvisible checks that calling lister(userID) returns zero items
// from "the other org". Used by every top-level service's isolation test in M2.
func (e *IsolationEnv) AssertInvisible(t *testing.T, describe string, userID int64,
	lister func(userID int64) (count int, err error)) {
	t.Helper()
	got, err := lister(userID)
	if err != nil {
		t.Fatalf("%s: lister err: %v", describe, err)
	}
	if got != 0 {
		t.Errorf("%s: expected 0 visible rows for user %d, got %d", describe, userID, got)
	}
}
