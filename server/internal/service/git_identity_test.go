package service

import (
	"context"
	"errors"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitIdentityService_Resolve_FallbackDomainWhenEmailUnset(t *testing.T) {
	db := openOrgTestDB(t)
	userID := newOrgTestUser(t, db, "alice", "member")

	svc := NewGitIdentityService(db)
	id, err := svc.Resolve(context.Background(), userID)
	require.NoError(t, err)
	// display_name was seeded to username by newOrgTestUser
	assert.Equal(t, "alice", id.Name)
	assert.Equal(t, "alice@niuniu.local", id.Email)
}

func TestGitIdentityService_Resolve_UsesRealEmailWhenSet(t *testing.T) {
	db := openOrgTestDB(t)
	userID := newOrgTestUser(t, db, "bob", "member")

	svc := NewGitIdentityService(db)
	require.NoError(t, svc.UpdateEmail(context.Background(), userID, "bob@example.com"))

	id, err := svc.Resolve(context.Background(), userID)
	require.NoError(t, err)
	assert.Equal(t, "bob", id.Name)
	assert.Equal(t, "bob@example.com", id.Email)
}

func TestGitIdentityService_Resolve_ZeroUserIDReturnsZeroIdentity(t *testing.T) {
	db := openOrgTestDB(t)
	svc := NewGitIdentityService(db)
	id, err := svc.Resolve(context.Background(), 0)
	require.NoError(t, err)
	assert.True(t, id.IsZero(), "expected zero Identity for userID=0")
}

func TestGitIdentityService_Resolve_UnknownUserReturnsZeroIdentity(t *testing.T) {
	db := openOrgTestDB(t)
	svc := NewGitIdentityService(db)
	id, err := svc.Resolve(context.Background(), 9999)
	require.NoError(t, err)
	assert.True(t, id.IsZero(), "expected zero Identity for unknown user")
}

func TestGitIdentityService_UpdateEmail_AllowsEmptyToClear(t *testing.T) {
	db := openOrgTestDB(t)
	userID := newOrgTestUser(t, db, "carol", "member")
	svc := NewGitIdentityService(db)

	require.NoError(t, svc.UpdateEmail(context.Background(), userID, "carol@x.com"))
	require.NoError(t, svc.UpdateEmail(context.Background(), userID, ""))

	got, err := svc.GetEmail(context.Background(), userID)
	require.NoError(t, err)
	assert.Equal(t, "", got)

	// Resolve should fall back again
	id, err := svc.Resolve(context.Background(), userID)
	require.NoError(t, err)
	assert.Equal(t, "carol@niuniu.local", id.Email)
}

func TestGitIdentityService_UpdateEmail_RejectsMalformed(t *testing.T) {
	db := openOrgTestDB(t)
	userID := newOrgTestUser(t, db, "dave", "member")
	svc := NewGitIdentityService(db)

	cases := []string{"not-an-email", "@x.com", "x@", "x@y", "x with space@y.z"}
	for _, c := range cases {
		err := svc.UpdateEmail(context.Background(), userID, c)
		assert.True(t, errors.Is(err, ErrInvalidEmail), "%q: expected ErrInvalidEmail, got %v", c, err)
	}
}

func TestGitIdentityService_UpdateEmail_RejectsEmbeddedControlChars(t *testing.T) {
	db := openOrgTestDB(t)
	userID := newOrgTestUser(t, db, "erin", "member")
	svc := NewGitIdentityService(db)

	// Embedded (not trailing — TrimSpace would strip those). The regex
	// already rejects \n/\r/\t via [^@\s], and the explicit check below
	// catches NUL which \s does not include.
	cases := []string{"a@b\n.c", "a\r@b.c", "a@b.c\x00"}
	for _, c := range cases {
		err := svc.UpdateEmail(context.Background(), userID, c)
		assert.True(t, errors.Is(err, ErrInvalidEmail), "%q: expected ErrInvalidEmail, got %v", c, err)
	}
}

func TestGitIdentityService_UpdateEmail_TrimsTrailingWhitespace(t *testing.T) {
	db := openOrgTestDB(t)
	userID := newOrgTestUser(t, db, "felix", "member")
	svc := NewGitIdentityService(db)

	// "alice@x.com\n" with trailing newline — TrimSpace normalizes; accepted.
	require.NoError(t, svc.UpdateEmail(context.Background(), userID, "alice@x.com\n"))
	got, err := svc.GetEmail(context.Background(), userID)
	require.NoError(t, err)
	assert.Equal(t, "alice@x.com", got)
}

func TestGitIdentityService_Resolve_DisplayNameWinsOverUsername(t *testing.T) {
	db := openOrgTestDB(t)
	userID := newOrgTestUser(t, db, "fred", "member")

	// Update display_name to something different from username
	_, err := db.ExecContext(context.Background(),
		`UPDATE users SET display_name = ? WHERE id = ?`,
		"Fred Niu", userID)
	require.NoError(t, err)

	svc := NewGitIdentityService(db)
	id, err := svc.Resolve(context.Background(), userID)
	require.NoError(t, err)
	assert.Equal(t, "Fred Niu", id.Name)
}

func TestEnvVarsForIdentity(t *testing.T) {
	assert.Nil(t, EnvVarsForIdentity(git.Identity{}))

	id := git.Identity{Name: "Alice", Email: "alice@x.com"}
	want := []string{
		"GIT_AUTHOR_NAME=Alice",
		"GIT_AUTHOR_EMAIL=alice@x.com",
		"GIT_COMMITTER_NAME=Alice",
		"GIT_COMMITTER_EMAIL=alice@x.com",
	}
	assert.Equal(t, want, EnvVarsForIdentity(id))
}
