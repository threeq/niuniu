package service

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/auth"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func setupAuthTestDB(t *testing.T) (*store.Queries, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	_, err = db.Exec(store.Schema)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return store.New(db), db
}

func TestAuthService_CreateUser(t *testing.T) {
	q, db := setupAuthTestDB(t)
	svc := NewAuthService(q, db, "test-secret", "15m", "168h")
	svc.SetPolicy(auth.Policy{MinLength: 6}) // permissive policy for legacy test fixtures

	user, err := svc.CreateUser(context.Background(), "admin", "password123", "Admin", "admin")
	require.NoError(t, err)
	assert.Equal(t, "admin", user.Username)
	assert.Equal(t, "admin", user.Role)
	assert.NotEqual(t, "password123", user.PasswordHash)
}

func TestAuthService_Login(t *testing.T) {
	q, db := setupAuthTestDB(t)
	svc := NewAuthService(q, db, "test-secret", "15m", "168h")
	svc.SetPolicy(auth.Policy{MinLength: 6}) // permissive policy for legacy test fixtures
	_, err := svc.CreateUser(context.Background(), "admin", "pass123", "Admin", "admin")
	require.NoError(t, err)

	tokens, err := svc.Login(context.Background(), "admin", "pass123")
	require.NoError(t, err)
	assert.NotEmpty(t, tokens.AccessToken)
	assert.NotEmpty(t, tokens.RefreshToken)

	claims, err := auth.ValidateToken(tokens.AccessToken, "test-secret")
	require.NoError(t, err)
	assert.Equal(t, "admin", claims.Username)
}

func TestAuthService_Login_WrongPassword(t *testing.T) {
	q, db := setupAuthTestDB(t)
	svc := NewAuthService(q, db, "test-secret", "15m", "168h")
	svc.SetPolicy(auth.Policy{MinLength: 6}) // permissive policy for legacy test fixtures
	svc.CreateUser(context.Background(), "admin", "pass123", "Admin", "admin")

	_, err := svc.Login(context.Background(), "admin", "wrong")
	assert.Error(t, err)
}

func TestAuthService_RefreshToken(t *testing.T) {
	q, db := setupAuthTestDB(t)
	svc := NewAuthService(q, db, "test-secret", "15m", "168h")
	svc.SetPolicy(auth.Policy{MinLength: 6}) // permissive policy for legacy test fixtures
	svc.CreateUser(context.Background(), "admin", "pass123", "Admin", "admin")

	tokens, err := svc.Login(context.Background(), "admin", "pass123")
	require.NoError(t, err)

	newTokens, err := svc.Refresh(context.Background(), tokens.RefreshToken)
	require.NoError(t, err)
	assert.NotEmpty(t, newTokens.AccessToken)
	assert.NotEmpty(t, newTokens.RefreshToken)
}

func TestAuthService_Logout(t *testing.T) {
	q, db := setupAuthTestDB(t)
	svc := NewAuthService(q, db, "test-secret", "15m", "168h")
	svc.SetPolicy(auth.Policy{MinLength: 6}) // permissive policy for legacy test fixtures
	svc.CreateUser(context.Background(), "admin", "pass123", "Admin", "admin")
	tokens, _ := svc.Login(context.Background(), "admin", "pass123")

	err := svc.Logout(context.Background(), tokens.RefreshToken)
	require.NoError(t, err)

	_, err = svc.Refresh(context.Background(), tokens.RefreshToken)
	assert.Error(t, err)
}

func setupAuthSvcDB(t *testing.T) (*store.Queries, *sql.DB) { return setupAuthTestDB(t) }

func ptr[T any](v T) *T { return &v }

// Note: the design doc spells the non-admin role as "user", but the actual
// users.role CHECK constraint is ('admin', 'member', 'viewer'). We use
// "member" in tests so they exercise real DB writes; downstream tasks should
// follow suit.

func TestAuthService_UpdateUser_HappyPath(t *testing.T) {
	q, db := setupAuthSvcDB(t)
	svc := NewAuthService(q, db, "test-secret", "15m", "168h")
	svc.SetPolicy(auth.Policy{MinLength: 6}) // permissive policy for legacy test fixtures
	u, err := svc.CreateUser(context.Background(), "bob", "pass123", "Bob", "member")
	require.NoError(t, err)

	updated, err := svc.UpdateUser(context.Background(), u.ID, u.ID /* caller */, ptr("Bob K."), ptr("admin"))
	require.NoError(t, err)
	assert.Equal(t, "Bob K.", updated.DisplayName)
	assert.Equal(t, "admin", updated.Role)
}

func TestAuthService_UpdateUser_CannotDemoteSelf(t *testing.T) {
	q, db := setupAuthSvcDB(t)
	svc := NewAuthService(q, db, "test-secret", "15m", "168h")
	svc.SetPolicy(auth.Policy{MinLength: 6}) // permissive policy for legacy test fixtures
	a1, _ := svc.CreateUser(context.Background(), "a1", "pass123", "A1", "admin")
	// add a second admin so the "last admin" guard does NOT fire — we want
	// the self-demote guard alone in this test
	_, _ = svc.CreateUser(context.Background(), "a2", "pass123", "A2", "admin")

	_, err := svc.UpdateUser(context.Background(), a1.ID, a1.ID, nil, ptr("member"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "demote yourself")
}

func TestAuthService_UpdateUser_LastAdminGuard(t *testing.T) {
	q, db := setupAuthSvcDB(t)
	svc := NewAuthService(q, db, "test-secret", "15m", "168h")
	svc.SetPolicy(auth.Policy{MinLength: 6}) // permissive policy for legacy test fixtures
	a1, _ := svc.CreateUser(context.Background(), "a1", "pass123", "A1", "admin")
	other, _ := svc.CreateUser(context.Background(), "u1", "pass123", "U1", "member")

	// caller is `other` (also admin in real life via middleware, but here the
	// caller arg is just used for the demote-self check, not authz)
	_, err := svc.UpdateUser(context.Background(), a1.ID, other.ID, nil, ptr("member"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "last admin")
}

func TestAuthService_UpdateUser_RejectsBadRole(t *testing.T) {
	q, db := setupAuthSvcDB(t)
	svc := NewAuthService(q, db, "test-secret", "15m", "168h")
	svc.SetPolicy(auth.Policy{MinLength: 6}) // permissive policy for legacy test fixtures
	u, _ := svc.CreateUser(context.Background(), "x", "pass123", "X", "member")
	_, err := svc.UpdateUser(context.Background(), u.ID, u.ID, nil, ptr("superadmin"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "role")
}

func TestAuthService_UpdateUser_RejectsAllNil(t *testing.T) {
	q, db := setupAuthSvcDB(t)
	svc := NewAuthService(q, db, "test-secret", "15m", "168h")
	svc.SetPolicy(auth.Policy{MinLength: 6}) // permissive policy for legacy test fixtures
	u, _ := svc.CreateUser(context.Background(), "x", "pass123", "X", "member")
	_, err := svc.UpdateUser(context.Background(), u.ID, u.ID, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no fields")
}

func TestAuthService_ResetPassword_HappyPath(t *testing.T) {
	q, db := setupAuthSvcDB(t)
	svc := NewAuthService(q, db, "test-secret", "15m", "168h")
	svc.SetPolicy(auth.Policy{MinLength: 6}) // permissive policy for legacy test fixtures
	u, _ := svc.CreateUser(context.Background(), "bob", "oldpass", "Bob", "member")

	// Mint a refresh token via Login so we can confirm it gets revoked.
	_, err := svc.Login(context.Background(), "bob", "oldpass")
	require.NoError(t, err)

	require.NoError(t, svc.ResetPassword(context.Background(), u.ID, "newpass"))

	// Old password no longer works.
	_, err = svc.Login(context.Background(), "bob", "oldpass")
	assert.Error(t, err)

	// New password works.
	_, err = svc.Login(context.Background(), "bob", "newpass")
	assert.NoError(t, err)
}

func TestAuthService_ResetPassword_RejectsShortPassword(t *testing.T) {
	q, db := setupAuthSvcDB(t)
	svc := NewAuthService(q, db, "test-secret", "15m", "168h")
	svc.SetPolicy(auth.Policy{MinLength: 6}) // permissive policy for legacy test fixtures
	u, _ := svc.CreateUser(context.Background(), "bob", "pass123", "Bob", "member")

	err := svc.ResetPassword(context.Background(), u.ID, "abc")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "password")
}

func TestAuthService_DeleteUser_LastAdminGuard(t *testing.T) {
	q, db := setupAuthSvcDB(t)
	svc := NewAuthService(q, db, "test-secret", "15m", "168h")
	svc.SetPolicy(auth.Policy{MinLength: 6}) // permissive policy for legacy test fixtures
	a1, _ := svc.CreateUser(context.Background(), "a1", "pass123", "A1", "admin")
	_, _ = svc.CreateUser(context.Background(), "u1", "pass123", "U1", "member")

	err := svc.DeleteUser(context.Background(), a1.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "last admin")
}

func TestAuthService_DeleteUser_NonAdminAllowed(t *testing.T) {
	q, db := setupAuthSvcDB(t)
	svc := NewAuthService(q, db, "test-secret", "15m", "168h")
	svc.SetPolicy(auth.Policy{MinLength: 6}) // permissive policy for legacy test fixtures
	_, _ = svc.CreateUser(context.Background(), "a1", "pass123", "A1", "admin")
	u1, _ := svc.CreateUser(context.Background(), "u1", "pass123", "U1", "member")

	require.NoError(t, svc.DeleteUser(context.Background(), u1.ID))
}

func TestAuthService_DeleteUser_NonLastAdminAllowed(t *testing.T) {
	q, db := setupAuthSvcDB(t)
	svc := NewAuthService(q, db, "test-secret", "15m", "168h")
	svc.SetPolicy(auth.Policy{MinLength: 6}) // permissive policy for legacy test fixtures
	a1, _ := svc.CreateUser(context.Background(), "a1", "pass123", "A1", "admin")
	_, _ = svc.CreateUser(context.Background(), "a2", "pass123", "A2", "admin")

	require.NoError(t, svc.DeleteUser(context.Background(), a1.ID))
}

func TestAuthService_DeleteUser_BlockedByOrgMembership(t *testing.T) {
	q, db := setupAuthSvcDB(t)
	svc := NewAuthService(q, db, "test-secret", "15m", "168h")
	svc.SetPolicy(auth.Policy{MinLength: 6}) // permissive policy for legacy test fixtures
	// Need 2 admins so the last-admin guard does not preempt
	_, _ = svc.CreateUser(context.Background(), "a1", "pass123", "A1", "admin")
	u, _ := svc.CreateUser(context.Background(), "u1", "pass123", "U1", "member")

	// Create an org and add u as a member.
	ctx := context.Background()
	res, err := q.CreateOrganization(ctx, store.CreateOrganizationParams{
		Slug: "acme", Name: "Acme", CreatedBy: u.ID,
	})
	require.NoError(t, err)
	require.NoError(t, q.AddOrgMember(ctx, store.AddOrgMemberParams{
		OrgID: res.ID, UserID: u.ID, Role: "owner",
	}))

	err = svc.DeleteUser(ctx, u.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "member of org")
}

func TestAuthService_DeleteUser_BlockedByOwnedProject(t *testing.T) {
	q, db := setupAuthSvcDB(t)
	svc := NewAuthService(q, db, "test-secret", "15m", "168h")
	svc.SetPolicy(auth.Policy{MinLength: 6}) // permissive policy for legacy test fixtures
	_, _ = svc.CreateUser(context.Background(), "a1", "pass123", "A1", "admin")
	u, _ := svc.CreateUser(context.Background(), "u1", "pass123", "U1", "member")

	ctx := context.Background()
	// Insert a personal-owned project directly so we trigger the
	// userOwnedResource guard. The project's name/status columns are
	// minimal; we don't care about the rest.
	_, err := db.ExecContext(ctx,
		`INSERT INTO projects (name, status, owner_type, owner_id) VALUES (?, 'active', 'user', ?)`,
		"p1", u.ID)
	require.NoError(t, err)

	err = svc.DeleteUser(ctx, u.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owns personal resources")
}
