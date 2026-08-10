package service

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// isolationEnv holds two orgs and two users with disjoint memberships:
//
//	OrgA <- UserA (owner)
//	OrgB <- UserB (owner)
type isolationEnv struct {
	DB    *sql.DB
	UserA int64
	UserB int64
	OrgA  int64
	OrgB  int64
}

func newIsolationEnv(t *testing.T) *isolationEnv {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatal(err)
	}
	store.Migrate(db)

	exec := func(q string, args ...any) int64 {
		r, err := db.Exec(q, args...)
		if err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
		id, _ := r.LastInsertId()
		return id
	}

	env := &isolationEnv{DB: db}
	env.UserA = exec(`INSERT INTO users (username, password_hash, role) VALUES ('user-a', 'x', 'admin')`)
	env.UserB = exec(`INSERT INTO users (username, password_hash, role) VALUES ('user-b', 'x', 'member')`)
	env.OrgA = exec(`INSERT INTO organizations (slug, name, created_by) VALUES ('org-a', 'OrgA', ?)`, env.UserA)
	env.OrgB = exec(`INSERT INTO organizations (slug, name, created_by) VALUES ('org-b', 'OrgB', ?)`, env.UserB)
	exec(`INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, 'owner')`, env.OrgA, env.UserA)
	exec(`INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, 'owner')`, env.OrgB, env.UserB)
	return env
}

func (e *isolationEnv) orgOwner(id int64) OwnerRef  { return OwnerRef{Type: "org", ID: id} }
func (e *isolationEnv) userOwner(id int64) OwnerRef { return OwnerRef{Type: "user", ID: id} }

// ─── existing test (unchanged) ───────────────────────────────────────────────

func TestAuthzCanAccessProject(t *testing.T) {
	db := openMCPTestDB(t)   // reuses helper from mcp_session_test.go
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO projects (id, name, status, owner_type, owner_id) VALUES (1, 'P', 'active', 'user', 42)`)

	authz := NewAuthz(store.New(db), db)
	owner, err := authz.CanAccessProject(context.Background(), 42, 1)
	if err != nil {
		t.Fatal(err)
	}
	if owner.Type != "user" || owner.ID != 42 {
		t.Errorf("owner = %+v; want {user 42}", owner)
	}
	if _, err := authz.CanAccessProject(context.Background(), 42, 99); err == nil {
		t.Error("expected error for unknown project")
	}
}

// ─── new M2 tests ─────────────────────────────────────────────────────────────

// TestAuthzCanAccessProjectPersonal verifies that a user's own personal project
// is accessible to themselves but forbidden to another user.
func TestAuthzCanAccessProjectPersonal(t *testing.T) {
	env := newIsolationEnv(t)
	authz := NewAuthz(store.New(env.DB), env.DB)

	var projID int64
	err := env.DB.QueryRow(
		`INSERT INTO projects (name, status, owner_type, owner_id) VALUES ('proj-a', 'active', 'user', ?) RETURNING id`,
		env.UserA).Scan(&projID)
	if err != nil {
		// SQLite <3.35 may not support RETURNING; fall back.
		env.DB.Exec(`INSERT INTO projects (name, status, owner_type, owner_id) VALUES ('proj-a', 'active', 'user', ?)`, env.UserA)
		env.DB.QueryRow(`SELECT id FROM projects WHERE name = 'proj-a'`).Scan(&projID)
	}

	// UserA can access their own project.
	if _, err := authz.CanAccessProject(context.Background(), env.UserA, projID); err != nil {
		t.Errorf("UserA should access own project: %v", err)
	}
	// UserB must be forbidden.
	if _, err := authz.CanAccessProject(context.Background(), env.UserB, projID); err != ErrForbidden {
		t.Errorf("UserB should be forbidden, got: %v", err)
	}
}

// TestAuthzCanAccessProjectOrg verifies that an org-owned project is accessible
// to org members but forbidden to outsiders.
func TestAuthzCanAccessProjectOrg(t *testing.T) {
	env := newIsolationEnv(t)
	authz := NewAuthz(store.New(env.DB), env.DB)

	env.DB.Exec(`INSERT INTO projects (name, status, owner_type, owner_id) VALUES ('proj-org-a', 'active', 'org', ?)`, env.OrgA)
	var projID int64
	env.DB.QueryRow(`SELECT id FROM projects WHERE name = 'proj-org-a'`).Scan(&projID)

	// UserA is owner of OrgA — should access.
	if _, err := authz.CanAccessProject(context.Background(), env.UserA, projID); err != nil {
		t.Errorf("UserA (member of OrgA) should access org project: %v", err)
	}
	// UserB is not a member of OrgA — should be forbidden.
	if _, err := authz.CanAccessProject(context.Background(), env.UserB, projID); err != ErrForbidden {
		t.Errorf("UserB (outsider) should be forbidden, got: %v", err)
	}
}

// TestAuthzAccessibleCaches verifies that two calls to Accessible for the same
// user return the same pointer (cache hit on the second call).
func TestAuthzAccessibleCaches(t *testing.T) {
	env := newIsolationEnv(t)
	authz := NewAuthz(store.New(env.DB), env.DB)

	first, err := authz.Accessible(context.Background(), env.UserA)
	if err != nil {
		t.Fatal(err)
	}
	second, err := authz.Accessible(context.Background(), env.UserA)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Error("expected same pointer on cache hit, got different pointers")
	}
	// Sanity: UserA should see OrgA.
	if !containsOrgID(first, env.OrgA) {
		t.Errorf("UserA should be member of OrgA; OrgIDs = %v", first.OrgIDs)
	}
}

// TestAuthzCanManageOrg verifies role-based access: owner and admin are allowed;
// plain member and non-members are denied.
func TestAuthzCanManageOrg(t *testing.T) {
	env := newIsolationEnv(t)
	authz := NewAuthz(store.New(env.DB), env.DB)

	// Add a third user as plain 'member' of OrgA, and UserB as 'admin'.
	var userC int64
	env.DB.QueryRow(
		`INSERT INTO users (username, password_hash, role) VALUES ('user-c', 'x', 'member') RETURNING id`,
	).Scan(&userC)
	if userC == 0 {
		env.DB.Exec(`INSERT INTO users (username, password_hash, role) VALUES ('user-c', 'x', 'member')`)
		env.DB.QueryRow(`SELECT id FROM users WHERE username = 'user-c'`).Scan(&userC)
	}
	env.DB.Exec(`INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, 'admin')`, env.OrgA, env.UserB)
	env.DB.Exec(`INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, 'member')`, env.OrgA, userC)

	// Owner (UserA) can manage OrgA.
	if err := authz.CanManageOrg(context.Background(), env.UserA, env.OrgA); err != nil {
		t.Errorf("owner should be able to manage org: %v", err)
	}
	// Admin (UserB) can manage OrgA.
	if err := authz.CanManageOrg(context.Background(), env.UserB, env.OrgA); err != nil {
		t.Errorf("admin should be able to manage org: %v", err)
	}
	// Plain member (userC, global role 'member') cannot manage OrgA.
	if err := authz.CanManageOrg(context.Background(), userC, env.OrgA); err != ErrForbidden {
		t.Errorf("member should be forbidden from managing org, got: %v", err)
	}
	// Non-member, non-global-admin (userC) cannot manage OrgB either.
	if err := authz.CanManageOrg(context.Background(), userC, env.OrgB); err != ErrForbidden {
		t.Errorf("non-member of OrgB should be forbidden, got: %v", err)
	}
	// Global admin (UserA holds global role 'admin') may manage ANY org, even
	// one they are not a member of.
	if err := authz.CanManageOrg(context.Background(), env.UserA, env.OrgB); err != nil {
		t.Errorf("global admin should be able to manage any org, got: %v", err)
	}
}

// TestAuthzEnsureOwnerWritable verifies that writing is gated correctly:
// personal owners must match userID; org owners require any membership role.
func TestAuthzEnsureOwnerWritable(t *testing.T) {
	env := newIsolationEnv(t)
	authz := NewAuthz(store.New(env.DB), env.DB)

	// Personal: UserA can write to their own space.
	if err := authz.EnsureOwnerWritable(context.Background(), env.UserA, env.userOwner(env.UserA)); err != nil {
		t.Errorf("UserA writing to own space: %v", err)
	}
	// Personal: UserA cannot write to UserB's space.
	if err := authz.EnsureOwnerWritable(context.Background(), env.UserA, env.userOwner(env.UserB)); err != ErrForbidden {
		t.Errorf("UserA writing to UserB space should be forbidden, got: %v", err)
	}

	// Org: UserA (owner of OrgA) can write.
	if err := authz.EnsureOwnerWritable(context.Background(), env.UserA, env.orgOwner(env.OrgA)); err != nil {
		t.Errorf("owner of OrgA should be writable: %v", err)
	}
	// Org: UserB (not a member of OrgA) cannot write.
	if err := authz.EnsureOwnerWritable(context.Background(), env.UserB, env.orgOwner(env.OrgA)); err != ErrForbidden {
		t.Errorf("non-member of OrgA should be forbidden from writing, got: %v", err)
	}
}

// ─── Task 5: IsProjectAdmin ───────────────────────────────────────────────────

// setupAuthzTest opens an in-memory SQLite DB with the full schema and returns
// a paired (*Authz, *sql.DB). Used by IsProjectAdmin tests below.
func setupAuthzTest(t *testing.T) (*Authz, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	store.Migrate(db)
	return NewAuthz(store.New(db), db), db
}

// authzCreateUser inserts a users row and returns the new id.
func authzCreateUser(t *testing.T, db *sql.DB, username string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO users (username, password_hash, display_name, role) VALUES (?, '', ?, 'member')`,
		username, username)
	if err != nil {
		t.Fatalf("create user %q: %v", username, err)
	}
	id, _ := res.LastInsertId()
	return id
}

// authzCreateOrg creates an organization owned by ownerUserID and seeds the
// owner row in org_members.
func authzCreateOrg(t *testing.T, db *sql.DB, slug string, ownerUserID int64) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO organizations (slug, name, created_by) VALUES (?, ?, ?)`,
		slug, slug, ownerUserID)
	if err != nil {
		t.Fatalf("create org %q: %v", slug, err)
	}
	id, _ := res.LastInsertId()
	if _, err := db.Exec(
		`INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, 'owner')`,
		id, ownerUserID); err != nil {
		t.Fatalf("seed owner for org %q: %v", slug, err)
	}
	return id
}

// authzAddOrgMember inserts an org_members row with the given role.
func authzAddOrgMember(t *testing.T, db *sql.DB, orgID, userID int64, role string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, ?)`,
		orgID, userID, role); err != nil {
		t.Fatalf("add member (org=%d user=%d role=%s): %v", orgID, userID, role, err)
	}
}

// authzCreateProject inserts a projects row with the given owner and returns
// the new project id.
func authzCreateProject(t *testing.T, db *sql.DB, ownerType string, ownerID int64, name string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO projects (name, status, owner_type, owner_id) VALUES (?, 'active', ?, ?)`,
		name, ownerType, ownerID)
	if err != nil {
		t.Fatalf("create project %q: %v", name, err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestIsProjectAdmin(t *testing.T) {
	a, db := setupAuthzTest(t)

	personalUser := authzCreateUser(t, db, "alice")
	personalProj := authzCreateProject(t, db, "user", personalUser, "p-personal")

	owner := authzCreateUser(t, db, "owner1")
	admin := authzCreateUser(t, db, "admin1")
	member := authzCreateUser(t, db, "member1")
	outsider := authzCreateUser(t, db, "outsider1")
	org := authzCreateOrg(t, db, "org1", owner)
	authzAddOrgMember(t, db, org, admin, "admin")
	authzAddOrgMember(t, db, org, member, "member")
	orgProj := authzCreateProject(t, db, "org", org, "p-org")

	cases := []struct {
		name   string
		userID int64
		projID int64
		want   bool
	}{
		{"personal-self", personalUser, personalProj, true},
		{"personal-other", outsider, personalProj, false},
		{"org-owner", owner, orgProj, true},
		{"org-admin", admin, orgProj, true},
		{"org-member", member, orgProj, false},
		{"org-outsider", outsider, orgProj, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := a.IsProjectAdmin(context.Background(), c.userID, c.projID)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

// TestAuthzEnsureOwnerWritableOrgMember verifies spec §6.5: any org membership
// role (including plain 'member') is sufficient for resource creation.
func TestAuthzEnsureOwnerWritableOrgMember(t *testing.T) {
	env := newIsolationEnv(t)
	authz := NewAuthz(store.New(env.DB), env.DB)

	// Add UserB as plain 'member' of OrgA (UserB is not owner/admin).
	env.DB.Exec(`INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, 'member')`, env.OrgA, env.UserB)

	// Plain member must be allowed to create resources in OrgA.
	if err := authz.EnsureOwnerWritable(context.Background(), env.UserB, env.orgOwner(env.OrgA)); err != nil {
		t.Errorf("plain org member should be writable (spec §6.5), got: %v", err)
	}

	// Non-member (UserB not in OrgB other than owner UserB is in OrgB — but OrgA UserB is just member)
	// Add a UserC with no membership in OrgA at all.
	var userC int64
	env.DB.QueryRow(
		`INSERT INTO users (username, password_hash, role) VALUES ('user-c2', 'x', 'member') RETURNING id`,
	).Scan(&userC)
	if userC == 0 {
		env.DB.Exec(`INSERT INTO users (username, password_hash, role) VALUES ('user-c2', 'x', 'member')`)
		env.DB.QueryRow(`SELECT id FROM users WHERE username = 'user-c2'`).Scan(&userC)
	}
	if err := authz.EnsureOwnerWritable(context.Background(), userC, env.orgOwner(env.OrgA)); err != ErrForbidden {
		t.Errorf("non-member of OrgA should be forbidden, got: %v", err)
	}
}

func TestAuthzIsOrgManagerSomewhere(t *testing.T) {
	env := newIsolationEnv(t)
	authz := NewAuthz(store.New(env.DB), env.DB)
	// UserA is owner of OrgA per existing seed.
	yes, err := authz.IsOrgManagerSomewhere(context.Background(), env.UserA)
	if err != nil {
		t.Fatal(err)
	}
	if !yes {
		t.Errorf("UserA should be manager somewhere")
	}
	// New user with no memberships — should be false.
	var newUserID int64
	env.DB.QueryRow(`INSERT INTO users (username, password_hash, role) VALUES ('nobody', 'x', 'member') RETURNING id`).Scan(&newUserID)
	if newUserID == 0 {
		res, _ := env.DB.Exec(`INSERT INTO users (username, password_hash, role) VALUES ('nobody', 'x', 'member')`)
		newUserID, _ = res.LastInsertId()
	}
	yes, err = authz.IsOrgManagerSomewhere(context.Background(), newUserID)
	if err != nil {
		t.Fatal(err)
	}
	if yes {
		t.Errorf("nobody should not be a manager")
	}
}

func TestAuthzIsOrgManagerSomewhereMemberRoleIsNotEnough(t *testing.T) {
	env := newIsolationEnv(t)
	authz := NewAuthz(store.New(env.DB), env.DB)
	var memberOnly int64
	env.DB.QueryRow(`INSERT INTO users (username, password_hash, role) VALUES ('justmember', 'x', 'member') RETURNING id`).Scan(&memberOnly)
	if memberOnly == 0 {
		res, _ := env.DB.Exec(`INSERT INTO users (username, password_hash, role) VALUES ('justmember', 'x', 'member')`)
		memberOnly, _ = res.LastInsertId()
	}
	env.DB.Exec(`INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, 'member')`, env.OrgA, memberOnly)
	yes, err := authz.IsOrgManagerSomewhere(context.Background(), memberOnly)
	if err != nil {
		t.Fatal(err)
	}
	if yes {
		t.Errorf("plain member should not pass IsOrgManagerSomewhere")
	}
}
