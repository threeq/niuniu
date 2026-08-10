package service

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newAdminUserServiceForTest assembles an AdminUserService over an in-memory
// SQLite DB. The notify/SSE/agent hooks are left nil (nil-safe on this path).
func newAdminUserServiceForTest(t *testing.T, db *sql.DB) (*AdminUserService, *store.Queries) {
	t.Helper()
	q := store.New(db)
	authz := NewAuthz(q, db)
	projectSvc := NewProjectService(q, db, nil, authz)
	repoSvc := NewRepositoryService(q, db, authz, t.TempDir())
	workspaceSvc := NewWorkspaceService(q, db, &config.WorkspaceConfig{}, t.TempDir(), nil, authz)
	orgSvc := NewOrgService(q, db, authz)
	svc := NewAdminUserService(q, db, t.TempDir(), projectSvc, workspaceSvc, repoSvc, orgSvc, authz)
	return svc, q
}

// seedUserResources inserts one of each personal-scoped resource for userID and
// returns the number of projects/workspaces/repositories created.
func seedUserResources(t *testing.T, db *sql.DB, userID int64) {
	t.Helper()
	ctx := context.Background()
	exec := func(q string, args ...any) {
		_, err := db.ExecContext(ctx, q, args...)
		require.NoError(t, err)
	}
	exec(`INSERT INTO projects (name, status, owner_type, owner_id) VALUES ('p1','active','user',?)`, userID)
	exec(`INSERT INTO projects (name, status, owner_type, owner_id) VALUES ('p2','active','user',?)`, userID)
	exec(`INSERT INTO repositories (name, path, owner_type, owner_id) VALUES ('r1','/tmp/r1','user',?)`, userID)
	exec(`INSERT INTO workspaces (name, path, owner_type, owner_id) VALUES ('w1','/tmp/w1','user',?)`, userID)
	exec(`INSERT INTO env_presets (name, owner_type, owner_id) VALUES ('e1','user',?)`, userID)
	exec(`INSERT INTO quick_actions (label, content, owner_type, owner_id) VALUES ('q1','c','user',?)`, userID)
	exec(`INSERT INTO agents (name, dir_path, owner_type, owner_id) VALUES ('a1','/tmp/a1','user',?)`, userID)
	exec(`INSERT INTO scenes (slug, display_name, definition, content_hash, owner_type, owner_id)
	      VALUES ('s1','Scene 1','{}','h','user',?)`, userID)
	exec(`INSERT INTO data_sources (owner_type, owner_id, user_id, name, kind, config)
	      VALUES ('user',?,?,'ds1','mysql','{}')`, userID, userID)
	exec(`INSERT INTO saved_queries (owner_type, owner_id, name) VALUES ('user',?,'sq1')`, userID)
	exec(`INSERT INTO dashboards (owner_type, owner_id, name) VALUES ('user',?,'db1')`, userID)
	exec(`INSERT INTO knowledge_bases (owner_type, owner_id, name) VALUES ('user',?,'kb1')`, userID)
}

func TestListUserResourcesCounts(t *testing.T) {
	db := openOrgTestDB(t)
	svc, _ := newAdminUserServiceForTest(t, db)

	userID := newOrgTestUser(t, db, "res-user", "member")
	otherID := newOrgTestUser(t, db, "other-user", "member")
	seedUserResources(t, db, userID)

	// Noise owned by another user + an org — must NOT be counted.
	_, err := db.Exec(`INSERT INTO projects (name, status, owner_type, owner_id) VALUES ('op','active','user',?)`, otherID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO agents (name, dir_path, owner_type, owner_id) VALUES ('oa','/tmp/oa','org',9)`)
	require.NoError(t, err)

	summary, err := svc.ListUserResources(context.Background(), userID)
	require.NoError(t, err)

	assert.Equal(t, "res-user", summary.User.Username)
	assert.Len(t, summary.Projects, 2)
	assert.Len(t, summary.Workspaces, 1)
	assert.Len(t, summary.Repositories, 1)
	assert.Equal(t, int64(1), summary.Counts.EnvPresets)
	assert.Equal(t, int64(1), summary.Counts.QuickActions)
	assert.Equal(t, int64(1), summary.Counts.Agents)
	assert.Equal(t, int64(1), summary.Counts.Scenes)
	assert.Equal(t, int64(1), summary.Counts.DataSources)
	assert.Equal(t, int64(1), summary.Counts.SavedQueries)
	assert.Equal(t, int64(1), summary.Counts.Dashboards)
	assert.Equal(t, int64(1), summary.Counts.KnowledgeBases)
	assert.Equal(t, int64(0), summary.Counts.HarnessSpecs)
	assert.Empty(t, summary.Orgs)
}

func TestListUserResourcesNotFound(t *testing.T) {
	db := openOrgTestDB(t)
	svc, _ := newAdminUserServiceForTest(t, db)

	_, err := svc.ListUserResources(context.Background(), 99999)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestPurgeUserGuardSelf(t *testing.T) {
	db := openOrgTestDB(t)
	svc, _ := newAdminUserServiceForTest(t, db)

	uid := newOrgTestUser(t, db, "self-user", "admin")
	_, err := svc.PurgeUser(context.Background(), uid, uid)
	var guard *PurgeGuardError
	require.ErrorAs(t, err, &guard)
	assert.Equal(t, "self", guard.Reason)
}

func TestPurgeUserGuardLastAdmin(t *testing.T) {
	db := openOrgTestDB(t)
	svc, _ := newAdminUserServiceForTest(t, db)

	admin := newOrgTestUser(t, db, "only-admin", "admin")
	actor := newOrgTestUser(t, db, "actor-member", "member")

	_, err := svc.PurgeUser(context.Background(), actor, admin)
	var guard *PurgeGuardError
	require.ErrorAs(t, err, &guard)
	assert.Equal(t, "last_admin", guard.Reason)
}

func TestPurgeUserGuardLastOwnerOfOrg(t *testing.T) {
	db := openOrgTestDB(t)
	svc, q := newAdminUserServiceForTest(t, db)

	actor := newOrgTestUser(t, db, "actor-admin", "admin")
	target := newOrgTestUser(t, db, "owner-admin", "admin") // 2 admins → last_admin guard passes

	orgSvc := NewOrgService(q, db, NewAuthz(q, db))
	org, err := orgSvc.Create(context.Background(), target, "owner-admin", "Sole Org", "sole-org", "")
	require.NoError(t, err)

	_, err = svc.PurgeUser(context.Background(), actor, target)
	var guard *PurgeGuardError
	require.ErrorAs(t, err, &guard)
	assert.Equal(t, "last_owner_of_org:"+org.Slug, guard.Reason)
}

func TestPurgeUserSuccess(t *testing.T) {
	db := openOrgTestDB(t)
	svc, q := newAdminUserServiceForTest(t, db)
	ctx := context.Background()

	actor := newOrgTestUser(t, db, "purge-actor", "admin")
	target := newOrgTestUser(t, db, "purge-target", "member")
	seedUserResources(t, db, target)

	// Actor owns an org; target joins as a plain member.
	orgSvc := NewOrgService(q, db, NewAuthz(q, db))
	org, err := orgSvc.Create(ctx, actor, "purge-actor", "Team Org", "team-org", "")
	require.NoError(t, err)
	require.NoError(t, orgSvc.AddMember(ctx, actor, "purge-actor", org.ID, target, "member"))

	summary, err := svc.PurgeUser(ctx, actor, target)
	require.NoError(t, err)

	assert.Equal(t, int64(2), summary.Projects)
	assert.Equal(t, int64(1), summary.Workspaces)
	assert.Equal(t, int64(1), summary.Repositories)
	assert.Equal(t, int64(1), summary.EnvPresets)
	assert.Equal(t, int64(1), summary.QuickActions)
	assert.Equal(t, int64(1), summary.Agents)
	assert.Equal(t, int64(1), summary.Scenes)
	assert.Equal(t, int64(1), summary.DataSources)
	assert.Equal(t, int64(1), summary.SavedQueries)
	assert.Equal(t, int64(1), summary.Dashboards)
	assert.Equal(t, int64(1), summary.KnowledgeBases)
	assert.Equal(t, int64(1), summary.OrgsLeft)

	// Account is gone.
	_, err = q.GetUserByID(ctx, target)
	assert.ErrorIs(t, err, sql.ErrNoRows)

	// Personal resources are gone.
	for _, tbl := range []string{"projects", "workspaces", "repositories", "env_presets", "quick_actions", "agents", "scenes", "data_sources", "saved_queries", "dashboards", "knowledge_bases"} {
		var n int64
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM `+tbl+` WHERE owner_type='user' AND owner_id=?`, target).Scan(&n))
		assert.Equalf(t, int64(0), n, "table %s should have no rows for purged user", tbl)
	}

	// Org membership is cleared.
	var members int64
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM org_members WHERE user_id=?`, target).Scan(&members))
	assert.Equal(t, int64(0), members)

	// The actor's org itself still exists.
	_, err = q.GetOrganization(ctx, org.ID)
	assert.NoError(t, err)
}

// TestPurgeUserReassignsCreatedOrg guards C2: a user who CREATED an org (but is
// not its sole owner) can be purged. organizations.created_by is a RESTRICT FK
// to users(id), so the final DeleteUser would otherwise abort the whole purge.
// PurgeUser must reassign created_by to a surviving owner first.
func TestPurgeUserReassignsCreatedOrg(t *testing.T) {
	db := openOrgTestDB(t)
	svc, q := newAdminUserServiceForTest(t, db)
	ctx := context.Background()

	actor := newOrgTestUser(t, db, "creator-actor", "admin")
	target := newOrgTestUser(t, db, "creator-target", "member")

	// target creates the org, then actor is added as a SECOND owner so target is
	// not the sole owner (last-owner guard passes).
	orgSvc := NewOrgService(q, db, NewAuthz(q, db))
	org, err := orgSvc.Create(ctx, target, "creator-target", "Created Org", "created-org", "")
	require.NoError(t, err)
	require.NoError(t, orgSvc.AddMember(ctx, target, "creator-target", org.ID, actor, "owner"))

	// Precondition: created_by points at the target.
	var createdBy int64
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT created_by FROM organizations WHERE id=?`, org.ID).Scan(&createdBy))
	require.Equal(t, target, createdBy)

	_, err = svc.PurgeUser(ctx, actor, target)
	require.NoError(t, err)

	// Account gone, org survives, created_by reassigned to the surviving owner.
	_, err = q.GetUserByID(ctx, target)
	assert.ErrorIs(t, err, sql.ErrNoRows)
	_, err = q.GetOrganization(ctx, org.ID)
	require.NoError(t, err)
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT created_by FROM organizations WHERE id=?`, org.ID).Scan(&createdBy))
	assert.Equal(t, actor, createdBy)
	assert.NotEqual(t, target, createdBy)
}
