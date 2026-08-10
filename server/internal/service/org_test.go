package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/pgtest"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// openOrgTestDB opens an in-memory SQLite database with the full schema applied.
func openOrgTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	_, err = db.Exec(store.Schema)
	require.NoError(t, err)
	store.Migrate(db)
	t.Cleanup(func() { db.Close() })
	return db
}

// newOrgTestUser inserts a user and returns their ID.
func newOrgTestUser(t *testing.T, db *sql.DB, username, globalRole string) int64 {
	t.Helper()
	var id int64
	err := db.QueryRowContext(context.Background(),
		`INSERT INTO users (username, password_hash, display_name, role) VALUES (?, 'x', ?, ?) RETURNING id`,
		username, username, globalRole).Scan(&id)
	if err != nil {
		// fallback for older SQLite
		_, err2 := db.ExecContext(context.Background(),
			`INSERT INTO users (username, password_hash, display_name, role) VALUES (?, 'x', ?, ?)`,
			username, username, globalRole)
		require.NoError(t, err2)
		require.NoError(t, db.QueryRowContext(context.Background(),
			`SELECT id FROM users WHERE username = ?`, username).Scan(&id))
	}
	return id
}

func TestOrgServiceCreate(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		svc, db, q := newOrgServiceForDriver(t, drv)

		callerID := newOrgTestUserDriver(t, db, "admin-user", "admin")

		org, err := svc.Create(context.Background(), callerID, "admin-user", "Acme Corp", "acme", "Test org")
		require.NoError(t, err)
		assert.Equal(t, "acme", org.Slug)
		assert.Equal(t, "Acme Corp", org.Name)

		// Caller should be the owner
		member, err := q.GetOrgMember(context.Background(), store.GetOrgMemberParams{
			OrgID:  org.ID,
			UserID: callerID,
		})
		require.NoError(t, err)
		assert.Equal(t, "owner", member.Role)

		// At least one audit entry should exist
		entries, err := svc.ListAuditLog(context.Background(), callerID, org.ID, 10, 0)
		require.NoError(t, err)
		assert.NotEmpty(t, entries)
		assert.Equal(t, "org.created", entries[0].Action)
	})
}

func TestOrgServiceDeleteRejectsNonEmpty(t *testing.T) {
	db := openOrgTestDB(t)
	q := store.New(db)
	authz := NewAuthz(q, db)
	svc := NewOrgService(q, db, authz)

	ownerID := newOrgTestUser(t, db, "owner1", "admin")

	org, err := svc.Create(context.Background(), ownerID, "owner1", "Delete Test Org", "delete-test", "")
	require.NoError(t, err)

	// Insert a project owned by this org
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO projects (name, status, owner_type, owner_id) VALUES ('proj1', 'active', 'org', ?)`,
		org.ID)
	require.NoError(t, err)

	// Delete should fail because projects table has a row for this org
	err = svc.Delete(context.Background(), ownerID, "owner1", org.ID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "projects")
}

// TestOrgServiceDeleteCleansConfigTables covers the production bug where an org
// that owns only auto-managed config rows could never be deleted. Such config
// rows (env_presets/quick_actions/agents) must NOT block deletion; DeleteOrg
// cleans them up instead.
func TestOrgServiceDeleteCleansConfigTables(t *testing.T) {
	db := openOrgTestDB(t)
	q := store.New(db)
	authz := NewAuthz(q, db)
	svc := NewOrgService(q, db, authz)

	ownerID := newOrgTestUser(t, db, "cfg-owner", "admin")
	org, err := svc.Create(context.Background(), ownerID, "cfg-owner", "Config Org", "config-org", "")
	require.NoError(t, err)

	// Org owns an env_preset (a cascade-cleaned config table).
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO env_presets (name, owner_type, owner_id) VALUES ('preset-x', 'org', ?)`, org.ID)
	require.NoError(t, err)

	// Delete must succeed despite the config row.
	err = svc.Delete(context.Background(), ownerID, "cfg-owner", org.ID)
	require.NoError(t, err)

	// The env_preset (cascade table) was cleaned up, not orphaned.
	var remaining int64
	require.NoError(t, db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM env_presets WHERE owner_type = 'org' AND owner_id = ?`, org.ID).Scan(&remaining))
	assert.Zero(t, remaining)
}

// TestOrgServiceDeleteEmptySucceeds guards against the regression where a stale
// entry in ownedTables (a table dropped by an earlier migration, e.g. the
// Phase 7 `harnesses` drop) makes the empty-check loop query a non-existent
// relation. On PG that surfaces as `relation "harnesses" does not exist`
// (SQLSTATE 42P01); on SQLite as `no such table`. An org with no owned
// resources must delete cleanly.
func TestOrgServiceDeleteEmptySucceeds(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		svc, db, _ := newOrgServiceForDriver(t, drv)

		ownerID := newOrgTestUserDriver(t, db, "empty-org-owner", "admin")

		org, err := svc.Create(context.Background(), ownerID, "empty-org-owner", "Empty Org", "empty-org", "")
		require.NoError(t, err)

		err = svc.Delete(context.Background(), ownerID, "empty-org-owner", org.ID)
		require.NoError(t, err)

		// The org row is gone.
		_, err = svc.Get(context.Background(), ownerID, org.ID)
		require.Error(t, err)
	})
}

// TestOrgServiceListAll_GlobalAdmin verifies a global admin sees every org
// (even ones they are not a member of), while a non-admin is forbidden.
func TestOrgServiceListAll_GlobalAdmin(t *testing.T) {
	db := openOrgTestDB(t)
	q := store.New(db)
	authz := NewAuthz(q, db)
	svc := NewOrgService(q, db, authz)

	adminID := newOrgTestUser(t, db, "global-admin", "admin")
	memberID := newOrgTestUser(t, db, "plain-member", "member")

	// Two orgs owned by the plain member; admin is a member of neither.
	orgA, err := svc.Create(context.Background(), memberID, "plain-member", "Org A", "org-a", "")
	require.NoError(t, err)
	orgB, err := svc.Create(context.Background(), memberID, "plain-member", "Org B", "org-b", "")
	require.NoError(t, err)

	// Global admin sees all orgs despite not being a member.
	all, err := svc.ListAll(context.Background(), adminID)
	require.NoError(t, err)
	got := map[int64]bool{}
	for _, o := range all {
		got[o.ID] = true
		assert.False(t, o.Role.Valid, "admin is not a member, role should be NULL")
	}
	assert.True(t, got[orgA.ID] && got[orgB.ID], "admin should see both orgs")

	// Non-admin is forbidden.
	_, err = svc.ListAll(context.Background(), memberID)
	assert.ErrorIs(t, err, ErrForbidden)
}

// TestOrgServiceListMembers_GlobalAdminBypass verifies a global admin can read
// any org's members without being a member, while a plain non-member cannot.
func TestOrgServiceListMembers_GlobalAdminBypass(t *testing.T) {
	db := openOrgTestDB(t)
	q := store.New(db)
	authz := NewAuthz(q, db)
	svc := NewOrgService(q, db, authz)

	adminID := newOrgTestUser(t, db, "admin2", "admin")
	ownerID := newOrgTestUser(t, db, "org-owner2", "member")
	outsiderID := newOrgTestUser(t, db, "outsider2", "member")

	org, err := svc.Create(context.Background(), ownerID, "org-owner2", "Members Org", "members-org", "")
	require.NoError(t, err)

	// Global admin (non-member) can read members and Get the org.
	members, err := svc.ListMembers(context.Background(), adminID, org.ID)
	require.NoError(t, err)
	assert.Len(t, members, 1)

	_, err = svc.Get(context.Background(), adminID, org.ID)
	require.NoError(t, err)

	// A plain non-member (non-admin) is still forbidden.
	_, err = svc.ListMembers(context.Background(), outsiderID, org.ID)
	assert.ErrorIs(t, err, ErrForbidden)
	_, err = svc.Get(context.Background(), outsiderID, org.ID)
	assert.ErrorIs(t, err, ErrForbidden)
}

// TestOrgServiceGlobalAdminCanManage verifies a global admin can fully manage an
// org they are not a member of: update it, add/remove members, and delete it.
func TestOrgServiceGlobalAdminCanManage(t *testing.T) {
	db := openOrgTestDB(t)
	q := store.New(db)
	authz := NewAuthz(q, db)
	svc := NewOrgService(q, db, authz)

	adminID := newOrgTestUser(t, db, "gadmin", "admin")
	ownerID := newOrgTestUser(t, db, "org-owner3", "member")
	newbieID := newOrgTestUser(t, db, "newbie3", "member")

	org, err := svc.Create(context.Background(), ownerID, "org-owner3", "Managed Org", "managed-org", "")
	require.NoError(t, err)

	// Admin (non-member) updates the org.
	_, err = svc.Update(context.Background(), adminID, "gadmin", org.ID, "Managed Org 2", "managed-org", "desc")
	require.NoError(t, err)

	// Admin (non-member) adds a member and changes their role.
	require.NoError(t, svc.AddMember(context.Background(), adminID, "gadmin", org.ID, newbieID, "member"))
	require.NoError(t, svc.UpdateMemberRole(context.Background(), adminID, "gadmin", org.ID, newbieID, "admin"))
	require.NoError(t, svc.RemoveMember(context.Background(), adminID, "gadmin", org.ID, newbieID))

	// Admin (non-member) deletes the empty org.
	require.NoError(t, svc.Delete(context.Background(), adminID, "gadmin", org.ID))

	// A plain non-member (non-admin) cannot manage.
	org2, err := svc.Create(context.Background(), ownerID, "org-owner3", "Other Org", "other-org", "")
	require.NoError(t, err)
	_, err = svc.Update(context.Background(), newbieID, "newbie3", org2.ID, "hacked", "other-org", "")
	assert.ErrorIs(t, err, ErrForbidden)
}

func TestOrgServiceRemoveLastOwnerFails(t *testing.T) {
	db := openOrgTestDB(t)
	q := store.New(db)
	authz := NewAuthz(q, db)
	svc := NewOrgService(q, db, authz)

	ownerID := newOrgTestUser(t, db, "sole-owner", "admin")

	org, err := svc.Create(context.Background(), ownerID, "sole-owner", "Last Owner Org", "last-owner", "")
	require.NoError(t, err)

	// Attempt to remove the sole owner
	err = svc.RemoveMember(context.Background(), ownerID, "sole-owner", org.ID, ownerID)
	assert.ErrorIs(t, err, ErrLastOwner)
}

func TestOrgServiceTransferOwnership(t *testing.T) {
	db := openOrgTestDB(t)
	q := store.New(db)
	authz := NewAuthz(q, db)
	svc := NewOrgService(q, db, authz)

	ownerID := newOrgTestUser(t, db, "original-owner", "admin")
	memberID := newOrgTestUser(t, db, "new-owner", "member")

	org, err := svc.Create(context.Background(), ownerID, "original-owner", "Transfer Org", "transfer-org", "")
	require.NoError(t, err)

	// Add memberID as a plain member first
	err = svc.AddMember(context.Background(), ownerID, "original-owner", org.ID, memberID, "member")
	require.NoError(t, err)

	// Transfer ownership
	err = svc.TransferOwnership(context.Background(), ownerID, "original-owner", org.ID, memberID)
	require.NoError(t, err)

	// Verify original owner is now admin
	callerMember, err := q.GetOrgMember(context.Background(), store.GetOrgMemberParams{OrgID: org.ID, UserID: ownerID})
	require.NoError(t, err)
	assert.Equal(t, "admin", callerMember.Role)

	// Verify target is now owner
	targetMember, err := q.GetOrgMember(context.Background(), store.GetOrgMemberParams{OrgID: org.ID, UserID: memberID})
	require.NoError(t, err)
	assert.Equal(t, "owner", targetMember.Role)
}

func TestOrgService_AddMember_ByUserID(t *testing.T) {
	db := openOrgTestDB(t)
	q := store.New(db)
	authz := NewAuthz(q, db)
	svc := NewOrgService(q, db, authz)

	ownerID := newOrgTestUser(t, db, "owner", "admin")
	org, err := svc.Create(context.Background(), ownerID, "owner", "Acme", "acme", "")
	require.NoError(t, err)

	targetID := newOrgTestUser(t, db, "target", "member")
	require.NoError(t, svc.AddMember(context.Background(), ownerID, "owner", org.ID, targetID, "member"))

	m, err := q.GetOrgMember(context.Background(), store.GetOrgMemberParams{
		OrgID: org.ID, UserID: targetID,
	})
	require.NoError(t, err)
	assert.Equal(t, "member", m.Role)
}

func TestOrgService_AddMember_UserNotFound(t *testing.T) {
	db := openOrgTestDB(t)
	q := store.New(db)
	authz := NewAuthz(q, db)
	svc := NewOrgService(q, db, authz)

	ownerID := newOrgTestUser(t, db, "owner", "admin")
	org, err := svc.Create(context.Background(), ownerID, "owner", "Acme", "acme", "")
	require.NoError(t, err)

	err = svc.AddMember(context.Background(), ownerID, "owner", org.ID, 999999, "member")
	assert.ErrorIs(t, err, ErrUserNotFound)
}

func TestOrgService_AddMember_DuplicateReturnsErrAlreadyMember(t *testing.T) {
	db := openOrgTestDB(t)
	q := store.New(db)
	authz := NewAuthz(q, db)
	svc := NewOrgService(q, db, authz)

	ownerID := newOrgTestUser(t, db, "owner", "admin")
	org, err := svc.Create(context.Background(), ownerID, "owner", "Acme", "acme", "")
	require.NoError(t, err)

	targetID := newOrgTestUser(t, db, "target", "member")
	require.NoError(t, svc.AddMember(context.Background(), ownerID, "owner", org.ID, targetID, "member"))

	err = svc.AddMember(context.Background(), ownerID, "owner", org.ID, targetID, "member")
	assert.ErrorIs(t, err, ErrAlreadyMember)
}

func TestOrgService_AddMember_InvalidRole(t *testing.T) {
	db := openOrgTestDB(t)
	q := store.New(db)
	authz := NewAuthz(q, db)
	svc := NewOrgService(q, db, authz)

	ownerID := newOrgTestUser(t, db, "owner", "admin")
	org, err := svc.Create(context.Background(), ownerID, "owner", "Acme", "acme", "")
	require.NoError(t, err)

	targetID := newOrgTestUser(t, db, "target", "member")
	err = svc.AddMember(context.Background(), ownerID, "owner", org.ID, targetID, "hacker")
	assert.ErrorIs(t, err, ErrInvalidRole)
}

func TestOrgServiceCreate_AutoSlugFromAsciiName(t *testing.T) {
	db := openOrgTestDB(t)
	q := store.New(db)
	authz := NewAuthz(q, db)
	svc := NewOrgService(q, db, authz)
	callerID := newOrgTestUser(t, db, "admin-user", "admin")

	org, err := svc.Create(context.Background(), callerID, "admin-user", "My Team", "", "")
	require.NoError(t, err)
	assert.Equal(t, "my-team", org.Slug)
}

func TestOrgServiceCreate_AutoSlugFromChineseNameFallsBack(t *testing.T) {
	db := openOrgTestDB(t)
	q := store.New(db)
	authz := NewAuthz(q, db)
	svc := NewOrgService(q, db, authz)
	callerID := newOrgTestUser(t, db, "admin-user", "admin")

	org, err := svc.Create(context.Background(), callerID, "admin-user", "测试组织", "", "")
	require.NoError(t, err)
	assert.Regexp(t, `^org-[0-9a-f]{6}$`, org.Slug)
}

func TestOrgServiceCreate_AutoSlugCollisionAppendsSuffix(t *testing.T) {
	db := openOrgTestDB(t)
	q := store.New(db)
	authz := NewAuthz(q, db)
	svc := NewOrgService(q, db, authz)
	callerID := newOrgTestUser(t, db, "admin-user", "admin")

	first, err := svc.Create(context.Background(), callerID, "admin-user", "My Team", "", "")
	require.NoError(t, err)
	assert.Equal(t, "my-team", first.Slug)

	second, err := svc.Create(context.Background(), callerID, "admin-user", "My Team", "", "")
	require.NoError(t, err)
	assert.Equal(t, "my-team-2", second.Slug)

	third, err := svc.Create(context.Background(), callerID, "admin-user", "My Team", "", "")
	require.NoError(t, err)
	assert.Equal(t, "my-team-3", third.Slug)
}

func TestOrgServiceCreate_RejectsEmptyName(t *testing.T) {
	db := openOrgTestDB(t)
	q := store.New(db)
	authz := NewAuthz(q, db)
	svc := NewOrgService(q, db, authz)
	callerID := newOrgTestUser(t, db, "admin-user", "admin")

	_, err := svc.Create(context.Background(), callerID, "admin-user", "", "", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "name")
}

func TestOrgServiceCreate_ExplicitSlugUnchanged(t *testing.T) {
	db := openOrgTestDB(t)
	q := store.New(db)
	authz := NewAuthz(q, db)
	svc := NewOrgService(q, db, authz)
	callerID := newOrgTestUser(t, db, "admin-user", "admin")

	org, err := svc.Create(context.Background(), callerID, "admin-user", "Whatever Name", "explicit-slug", "")
	require.NoError(t, err)
	assert.Equal(t, "explicit-slug", org.Slug)
}

// When the user explicitly typed a slug that collides with an existing org,
// we do NOT silently rename it. The caller gets ErrSlugConflict so the API
// layer can return 409.
func TestOrgServiceCreate_ExplicitSlugCollisionReturnsConflict(t *testing.T) {
	db := openOrgTestDB(t)
	q := store.New(db)
	authz := NewAuthz(q, db)
	svc := NewOrgService(q, db, authz)
	callerID := newOrgTestUser(t, db, "admin-user", "admin")

	_, err := svc.Create(context.Background(), callerID, "admin-user", "First", "shared", "")
	require.NoError(t, err)

	_, err = svc.Create(context.Background(), callerID, "admin-user", "Second", "shared", "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSlugConflict),
		"expected ErrSlugConflict for explicit slug collision, got %v", err)
}

// TestOrgService_RemoveMember_ClearsIssueAssignees verifies that removing a
// member from an org clears that user's assignments on issues belonging to
// projects owned by *that* org, while leaving their assignments in other orgs
// or in personal projects untouched. Also confirms that an audit row with
// action="member.removed.assignee_cleanup" and matching cleared_count is
// written when at least one row is cleared.
func TestOrgService_RemoveMember_ClearsIssueAssignees(t *testing.T) {
	db := openOrgTestDB(t)
	q := store.New(db)
	authz := NewAuthz(q, db)
	s := NewOrgService(q, db, authz)
	ctx := context.Background()

	owner := orgRMCreateUser(t, db, "owner-rmica")
	member := orgRMCreateUser(t, db, "member-rmica")
	other := orgRMCreateUser(t, db, "other-rmica")
	org := orgRMCreateOrg(t, db, "o-rmica", owner)
	otherOrg := orgRMCreateOrg(t, db, "o2-rmica", other)
	orgRMAddOrgMember(t, db, org, member, "member")
	orgRMAddOrgMember(t, db, otherOrg, member, "member")

	proj1 := orgRMCreateProject(t, db, "org", org, "p1-rmica")
	col1 := orgRMCreateColumn(t, db, proj1, "todo")
	issue1 := orgRMCreateIssue(t, db, col1, "i1")
	orgRMAssign(t, db, issue1, member)

	proj2 := orgRMCreateProject(t, db, "org", otherOrg, "p2-rmica")
	col2 := orgRMCreateColumn(t, db, proj2, "todo")
	issue2 := orgRMCreateIssue(t, db, col2, "i2")
	orgRMAssign(t, db, issue2, member)

	personalProj := orgRMCreateProject(t, db, "user", member, "personal-rmica")
	personalCol := orgRMCreateColumn(t, db, personalProj, "todo")
	personalIssue := orgRMCreateIssue(t, db, personalCol, "ip")
	orgRMAssign(t, db, personalIssue, member)

	if err := s.RemoveMember(ctx, owner, "owner-rmica", org, member); err != nil {
		t.Fatal(err)
	}

	if orgRMIsAssigned(t, db, issue1, member) {
		t.Errorf("issue1 still has member assigned")
	}
	if !orgRMIsAssigned(t, db, issue2, member) {
		t.Errorf("issue2 (other org) lost assignment unexpectedly")
	}
	if !orgRMIsAssigned(t, db, personalIssue, member) {
		t.Errorf("personal issue lost assignment unexpectedly")
	}

	// Audit row check.
	var found bool
	var payload string
	rows, err := db.Query(`SELECT action, payload FROM org_audit_log WHERE org_id = ?`, org)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var action, p string
		if err := rows.Scan(&action, &p); err != nil {
			t.Fatal(err)
		}
		if action == "member.removed.assignee_cleanup" {
			found = true
			payload = p
		}
	}
	if !found {
		t.Errorf("expected member.removed.assignee_cleanup audit row, got none")
	}
	if !strings.Contains(payload, `"cleared_count":1`) {
		t.Errorf("payload missing cleared_count=1: %q", payload)
	}
}

// File-local helpers for TestOrgService_RemoveMember_ClearsIssueAssignees.
// Named with the orgRM prefix so they don't collide with package-shared
// fixtures and are easy to grep in isolation.
func orgRMCreateUser(t *testing.T, db *sql.DB, username string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO users (username, password_hash, display_name, role) VALUES (?, '', ?, 'member')`,
		username, username)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func orgRMCreateOrg(t *testing.T, db *sql.DB, name string, ownerUserID int64) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO organizations (slug, name, created_by) VALUES (?, ?, ?)`,
		name, name, ownerUserID)
	if err != nil {
		t.Fatalf("create org: %v", err)
	}
	id, _ := res.LastInsertId()
	if _, err := db.Exec(
		`INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, 'owner')`,
		id, ownerUserID); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	return id
}

func orgRMAddOrgMember(t *testing.T, db *sql.DB, orgID, userID int64, role string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, ?)`,
		orgID, userID, role); err != nil {
		t.Fatalf("add member: %v", err)
	}
}

func orgRMCreateProject(t *testing.T, db *sql.DB, ownerType string, ownerID int64, name string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO projects (name, status, owner_type, owner_id) VALUES (?, 'active', ?, ?)`,
		name, ownerType, ownerID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func orgRMCreateColumn(t *testing.T, db *sql.DB, projectID int64, name string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO columns (project_id, name, position) VALUES (?, ?, 0)`,
		projectID, name)
	if err != nil {
		t.Fatalf("create column: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func orgRMCreateIssue(t *testing.T, db *sql.DB, columnID int64, title string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO issues (column_id, title, position) VALUES (?, ?, 0)`,
		columnID, title)
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func orgRMAssign(t *testing.T, db *sql.DB, issueID, userID int64) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO issue_assignees (issue_id, user_id) VALUES (?, ?)`,
		issueID, userID); err != nil {
		t.Fatalf("assign: %v", err)
	}
}

func orgRMIsAssigned(t *testing.T, db *sql.DB, issueID, userID int64) bool {
	t.Helper()
	var n int
	_ = db.QueryRow(
		`SELECT COUNT(*) FROM issue_assignees WHERE issue_id=? AND user_id=?`,
		issueID, userID).Scan(&n)
	return n > 0
}
