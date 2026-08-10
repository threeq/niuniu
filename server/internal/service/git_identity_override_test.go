package service

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// insertTestRepository inserts a minimal repositories row and returns the id.
func insertTestRepository(t *testing.T, db *sql.DB, name, path string, ownerID int64) int64 {
	t.Helper()
	var id int64
	err := db.QueryRowContext(context.Background(),
		`INSERT INTO repositories (name, path, owner_type, owner_id) VALUES (?, ?, 'user', ?) RETURNING id`,
		name, path, ownerID,
	).Scan(&id)
	require.NoError(t, err)
	return id
}

func TestResolveForRepository_NoOverride_FallsBackToGlobal(t *testing.T) {
	db := openOrgTestDB(t)
	userID := newOrgTestUser(t, db, "alice", "member")
	svc := NewGitIdentityService(db)

	require.NoError(t, svc.UpdateEmail(context.Background(), userID, "alice@global.com"))
	repoID := insertTestRepository(t, db, "Repo", "/tmp/r", userID)

	id, err := svc.ResolveForRepository(context.Background(), userID, repoID)
	require.NoError(t, err)
	assert.Equal(t, "alice", id.Name)
	assert.Equal(t, "alice@global.com", id.Email)
}

func TestResolveForRepository_FullOverride_Wins(t *testing.T) {
	db := openOrgTestDB(t)
	userID := newOrgTestUser(t, db, "alice", "member")
	svc := NewGitIdentityService(db)
	require.NoError(t, svc.UpdateEmail(context.Background(), userID, "alice@global.com"))
	repoID := insertTestRepository(t, db, "Repo", "/tmp/r", userID)

	_, err := svc.UpsertOverride(context.Background(), userID, repoID, "Alice (Work)", "alice@work.com")
	require.NoError(t, err)

	id, err := svc.ResolveForRepository(context.Background(), userID, repoID)
	require.NoError(t, err)
	assert.Equal(t, "Alice (Work)", id.Name)
	assert.Equal(t, "alice@work.com", id.Email)
}

func TestResolveForRepository_PartialOverride_FallsThroughEmptyField(t *testing.T) {
	db := openOrgTestDB(t)
	userID := newOrgTestUser(t, db, "alice", "member")
	svc := NewGitIdentityService(db)
	require.NoError(t, svc.UpdateEmail(context.Background(), userID, "alice@global.com"))
	repoID := insertTestRepository(t, db, "Repo", "/tmp/r", userID)

	// Override only the email; name falls back to global.
	_, err := svc.UpsertOverride(context.Background(), userID, repoID, "", "alice@personal.com")
	require.NoError(t, err)

	id, err := svc.ResolveForRepository(context.Background(), userID, repoID)
	require.NoError(t, err)
	assert.Equal(t, "alice", id.Name, "empty name falls back to global username")
	assert.Equal(t, "alice@personal.com", id.Email)
}

func TestResolveForRepository_ZeroRepoID_ReturnsGlobal(t *testing.T) {
	db := openOrgTestDB(t)
	userID := newOrgTestUser(t, db, "alice", "member")
	svc := NewGitIdentityService(db)
	require.NoError(t, svc.UpdateEmail(context.Background(), userID, "alice@global.com"))

	id, err := svc.ResolveForRepository(context.Background(), userID, 0)
	require.NoError(t, err)
	assert.Equal(t, "alice@global.com", id.Email)
}

func TestUpsertOverride_RejectsBadEmail(t *testing.T) {
	db := openOrgTestDB(t)
	userID := newOrgTestUser(t, db, "alice", "member")
	svc := NewGitIdentityService(db)
	repoID := insertTestRepository(t, db, "Repo", "/tmp/r", userID)

	_, err := svc.UpsertOverride(context.Background(), userID, repoID, "x", "not-an-email")
	assert.True(t, errors.Is(err, ErrInvalidEmail))
}

func TestUpsertOverride_RejectsControlCharsInName(t *testing.T) {
	db := openOrgTestDB(t)
	userID := newOrgTestUser(t, db, "alice", "member")
	svc := NewGitIdentityService(db)
	repoID := insertTestRepository(t, db, "Repo", "/tmp/r", userID)

	for _, badName := range []string{"Alice\nEvil", "Bob\rCarol", "\x00name"} {
		_, err := svc.UpsertOverride(context.Background(), userID, repoID, badName, "x@y.z")
		assert.True(t, errors.Is(err, ErrInvalidName),
			"name=%q expected ErrInvalidName (NOT ErrInvalidEmail), got %v", badName, err)
	}
}

func TestUpsertOverride_IsIdempotent(t *testing.T) {
	db := openOrgTestDB(t)
	userID := newOrgTestUser(t, db, "alice", "member")
	svc := NewGitIdentityService(db)
	repoID := insertTestRepository(t, db, "Repo", "/tmp/r", userID)

	o1, err := svc.UpsertOverride(context.Background(), userID, repoID, "X", "x@a.com")
	require.NoError(t, err)
	o2, err := svc.UpsertOverride(context.Background(), userID, repoID, "Y", "y@a.com")
	require.NoError(t, err)
	assert.Equal(t, o1.ID, o2.ID, "second upsert should mutate same row, not create new")
	assert.Equal(t, "Y", o2.Name)
	assert.Equal(t, "y@a.com", o2.Email)
}

func TestListOverrides_PerUser(t *testing.T) {
	db := openOrgTestDB(t)
	alice := newOrgTestUser(t, db, "alice", "member")
	bob := newOrgTestUser(t, db, "bob", "member")
	svc := NewGitIdentityService(db)
	r1 := insertTestRepository(t, db, "R1", "/r1", alice)
	r2 := insertTestRepository(t, db, "R2", "/r2", alice)

	_, _ = svc.UpsertOverride(context.Background(), alice, r1, "AliceA", "a@a.com")
	_, _ = svc.UpsertOverride(context.Background(), alice, r2, "AliceB", "b@a.com")
	_, _ = svc.UpsertOverride(context.Background(), bob, r1, "Bobby", "bob@b.com")

	aliceList, err := svc.ListOverrides(context.Background(), alice)
	require.NoError(t, err)
	assert.Len(t, aliceList, 2)

	bobList, err := svc.ListOverrides(context.Background(), bob)
	require.NoError(t, err)
	assert.Len(t, bobList, 1)
	assert.Equal(t, "Bobby", bobList[0].Name)
}

func TestDeleteOverride_FallsBackToGlobal(t *testing.T) {
	db := openOrgTestDB(t)
	userID := newOrgTestUser(t, db, "alice", "member")
	svc := NewGitIdentityService(db)
	require.NoError(t, svc.UpdateEmail(context.Background(), userID, "alice@global.com"))
	repoID := insertTestRepository(t, db, "Repo", "/tmp/r", userID)

	_, err := svc.UpsertOverride(context.Background(), userID, repoID, "Override", "override@x.com")
	require.NoError(t, err)
	require.NoError(t, svc.DeleteOverride(context.Background(), userID, repoID))

	id, err := svc.ResolveForRepository(context.Background(), userID, repoID)
	require.NoError(t, err)
	assert.Equal(t, "alice@global.com", id.Email, "after delete, falls back to global")
}

func TestDeleteOverride_NoSuchOverrideReturnsErrNoRows(t *testing.T) {
	db := openOrgTestDB(t)
	userID := newOrgTestUser(t, db, "alice", "member")
	svc := NewGitIdentityService(db)
	repoID := insertTestRepository(t, db, "Repo", "/tmp/r", userID)

	err := svc.DeleteOverride(context.Background(), userID, repoID)
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

func TestGetOverride_ReturnsNilWhenAbsent(t *testing.T) {
	db := openOrgTestDB(t)
	userID := newOrgTestUser(t, db, "alice", "member")
	svc := NewGitIdentityService(db)
	repoID := insertTestRepository(t, db, "Repo", "/tmp/r", userID)

	o, err := svc.GetOverride(context.Background(), userID, repoID)
	require.NoError(t, err)
	assert.Nil(t, o)
}
