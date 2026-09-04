package service

import (
	"context"
	"database/sql"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initTestRepo creates a real git repo at dir with a deliberately "wrong"
// local identity, so a test can tell "niuniu pinned this" from "git fell back".
func initTestRepo(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, exec.Command("git", "init", "-q", dir).Run())
	for _, kv := range [][2]string{{"user.name", "AmbientName"}, {"user.email", "ambient@example.com"}} {
		require.NoError(t, exec.Command("git", "-C", dir, "config", "--local", kv[0], kv[1]).Run())
	}
}

func localConfig(t *testing.T, dir, key string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "config", "--local", "--get", key).Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

// insertTestWorktree links a repo into a workspace at an on-disk path.
func insertTestWorktree(t *testing.T, db *sql.DB, workspaceID, repoID int64, path string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO worktrees (workspace_id, repository_id, worktree_path, branch) VALUES (?, ?, ?, 'main')`,
		workspaceID, repoID, path)
	require.NoError(t, err)
}

func insertTestWorkspace(t *testing.T, db *sql.DB, ownerID int64, path string) int64 {
	t.Helper()
	var id int64
	err := db.QueryRowContext(context.Background(),
		`INSERT INTO workspaces (name, path, owner_type, owner_id) VALUES ('ws', ?, 'user', ?) RETURNING id`,
		path, ownerID).Scan(&id)
	require.NoError(t, err)
	return id
}

// A workspace whose repos all resolve to the same signature needs no local
// pinning: env injection covers it, and also covers repos added after spawn.
func TestSyncWorktreeIdentities_NoOverride_ReportsNoPinningNeeded(t *testing.T) {
	db := openOrgTestDB(t)
	q := store.New(db)
	ctx := context.Background()
	userID := newOrgTestUser(t, db, "alice", "member")
	svc := NewGitIdentityService(db)
	require.NoError(t, svc.UpdateEmail(ctx, userID, "alice@global.com"))

	base := t.TempDir()
	wtPath := filepath.Join(base, "repo-a")
	initTestRepo(t, wtPath)
	repoID := insertTestRepository(t, db, "RepoA", wtPath, userID)
	wsID := insertTestWorkspace(t, db, userID, base)
	insertTestWorktree(t, db, wsID, repoID, wtPath)

	pinned, err := svc.SyncWorktreeIdentities(ctx, q, wsID, userID)
	require.NoError(t, err)
	assert.False(t, pinned, "no per-repo override exists, so env injection should be used")

	// The global identity is still written locally — harmless, and it means a
	// commit is attributed correctly even if env injection is skipped.
	assert.Equal(t, "alice", localConfig(t, wtPath, "user.name"))
	assert.Equal(t, "alice@global.com", localConfig(t, wtPath, "user.email"))
}

// The case env vars cannot express: two repos in one workspace, different
// signatures. Local config must carry the difference, and the caller must be
// told to withhold the process-wide env vars.
func TestSyncWorktreeIdentities_PerRepoOverride_PinsEachWorktree(t *testing.T) {
	db := openOrgTestDB(t)
	q := store.New(db)
	ctx := context.Background()
	userID := newOrgTestUser(t, db, "alice", "member")
	svc := NewGitIdentityService(db)
	require.NoError(t, svc.UpdateEmail(ctx, userID, "alice@global.com"))

	base := t.TempDir()
	pathA := filepath.Join(base, "repo-a")
	pathB := filepath.Join(base, "repo-b")
	initTestRepo(t, pathA)
	initTestRepo(t, pathB)
	repoA := insertTestRepository(t, db, "RepoA", pathA, userID)
	repoB := insertTestRepository(t, db, "RepoB", pathB, userID)
	wsID := insertTestWorkspace(t, db, userID, base)
	insertTestWorktree(t, db, wsID, repoA, pathA)
	insertTestWorktree(t, db, wsID, repoB, pathB)

	// Only repo B gets a work identity.
	_, err := svc.UpsertOverride(ctx, userID, repoB, "Alice Work", "alice@work.com")
	require.NoError(t, err)

	pinned, err := svc.SyncWorktreeIdentities(ctx, q, wsID, userID)
	require.NoError(t, err)
	assert.True(t, pinned, "an override exists, so env vars must be withheld")

	assert.Equal(t, "alice", localConfig(t, pathA, "user.name"))
	assert.Equal(t, "alice@global.com", localConfig(t, pathA, "user.email"))
	assert.Equal(t, "Alice Work", localConfig(t, pathB, "user.name"))
	assert.Equal(t, "alice@work.com", localConfig(t, pathB, "user.email"))
}

// A partial override (email only) still counts as needing local pinning, since
// the resulting signature differs from the global one.
func TestSyncWorktreeIdentities_PartialOverride_CountsAsPinned(t *testing.T) {
	db := openOrgTestDB(t)
	q := store.New(db)
	ctx := context.Background()
	userID := newOrgTestUser(t, db, "alice", "member")
	svc := NewGitIdentityService(db)
	require.NoError(t, svc.UpdateEmail(ctx, userID, "alice@global.com"))

	base := t.TempDir()
	wtPath := filepath.Join(base, "repo-a")
	initTestRepo(t, wtPath)
	repoID := insertTestRepository(t, db, "RepoA", wtPath, userID)
	wsID := insertTestWorkspace(t, db, userID, base)
	insertTestWorktree(t, db, wsID, repoID, wtPath)

	_, err := svc.UpsertOverride(ctx, userID, repoID, "", "alice@work.com")
	require.NoError(t, err)

	pinned, err := svc.SyncWorktreeIdentities(ctx, q, wsID, userID)
	require.NoError(t, err)
	assert.True(t, pinned)
	assert.Equal(t, "alice", localConfig(t, wtPath, "user.name"), "empty name falls through to global")
	assert.Equal(t, "alice@work.com", localConfig(t, wtPath, "user.email"))
}

// Zero user (autonomous run, no recorded actor) must not rewrite anyone's repo
// config — the ambient identity stays untouched.
func TestSyncWorktreeIdentities_ZeroUser_LeavesConfigAlone(t *testing.T) {
	db := openOrgTestDB(t)
	q := store.New(db)
	ctx := context.Background()
	owner := newOrgTestUser(t, db, "alice", "member")
	svc := NewGitIdentityService(db)

	base := t.TempDir()
	wtPath := filepath.Join(base, "repo-a")
	initTestRepo(t, wtPath)
	repoID := insertTestRepository(t, db, "RepoA", wtPath, owner)
	wsID := insertTestWorkspace(t, db, owner, base)
	insertTestWorktree(t, db, wsID, repoID, wtPath)

	pinned, err := svc.SyncWorktreeIdentities(ctx, q, wsID, 0)
	require.NoError(t, err)
	assert.False(t, pinned)
	assert.Equal(t, "AmbientName", localConfig(t, wtPath, "user.name"))
}

// A worktree whose directory is gone must not abort the spawn: the remaining
// worktrees still get pinned.
func TestSyncWorktreeIdentities_MissingWorktreeDir_IsSkipped(t *testing.T) {
	db := openOrgTestDB(t)
	q := store.New(db)
	ctx := context.Background()
	userID := newOrgTestUser(t, db, "alice", "member")
	svc := NewGitIdentityService(db)
	require.NoError(t, svc.UpdateEmail(ctx, userID, "alice@global.com"))

	base := t.TempDir()
	good := filepath.Join(base, "repo-good")
	initTestRepo(t, good)
	gone := filepath.Join(base, "repo-gone") // never created

	repoGood := insertTestRepository(t, db, "Good", good, userID)
	repoGone := insertTestRepository(t, db, "Gone", gone, userID)
	wsID := insertTestWorkspace(t, db, userID, base)
	insertTestWorktree(t, db, wsID, repoGood, good)
	insertTestWorktree(t, db, wsID, repoGone, gone)

	_, err := svc.SyncWorktreeIdentities(ctx, q, wsID, userID)
	require.NoError(t, err, "a missing worktree must not fail the whole sync")
	assert.Equal(t, "alice@global.com", localConfig(t, good, "user.email"))
}

// --- 全局配置 tier reachability (个人设置 > 仓库设置 > 全局配置) ---

// clearDisplayName models a user who registered without a display name and
// never set an email — i.e. configured no git signature in niuniu at all.
// The shared newOrgTestUser helper always fills display_name, which would
// otherwise mask the very case these tests exist to cover.
func clearDisplayName(t *testing.T, db *sql.DB, userID int64) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`UPDATE users SET display_name = '' WHERE id = ?`, userID)
	require.NoError(t, err)
}

// A user who configured nothing in niuniu must NOT be pinned to the synthetic
// <username>@niuniu.local: that would shadow their own git config and make the
// third precedence tier unreachable.
func TestResolveConfigured_UnconfiguredUser_YieldsZero(t *testing.T) {
	db := openOrgTestDB(t)
	ctx := context.Background()
	userID := newOrgTestUser(t, db, "alice", "member")
	clearDisplayName(t, db, userID)
	svc := NewGitIdentityService(db)

	id, err := svc.ResolveConfigured(ctx, userID, 0)
	require.NoError(t, err)
	assert.True(t, id.IsZero(), "unconfigured user must fall through to git config, got %+v", id)

	// Resolve (display path) still synthesizes — that contract is unchanged.
	shown, err := svc.Resolve(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, "alice@niuniu.local", shown.Email)
}

// Setting only the global email counts as configured: 个人设置 applies.
func TestResolveConfigured_GlobalEmailSet_IsHonored(t *testing.T) {
	db := openOrgTestDB(t)
	ctx := context.Background()
	userID := newOrgTestUser(t, db, "alice", "member")
	svc := NewGitIdentityService(db)
	require.NoError(t, svc.UpdateEmail(ctx, userID, "alice@personal.com"))

	id, err := svc.ResolveConfigured(ctx, userID, 0)
	require.NoError(t, err)
	assert.Equal(t, "alice@personal.com", id.Email)
	assert.Equal(t, "alice", id.Name)
}

// A per-repo override applies even when the user set no global identity.
func TestResolveConfigured_OverrideOnly_IsHonored(t *testing.T) {
	db := openOrgTestDB(t)
	ctx := context.Background()
	userID := newOrgTestUser(t, db, "alice", "member")
	svc := NewGitIdentityService(db)
	clearDisplayName(t, db, userID)
	repoID := insertTestRepository(t, db, "R", "/tmp/r", userID)

	_, err := svc.UpsertOverride(ctx, userID, repoID, "Alice Work", "alice@work.com")
	require.NoError(t, err)

	id, err := svc.ResolveConfigured(ctx, userID, repoID)
	require.NoError(t, err)
	assert.Equal(t, "Alice Work", id.Name)
	assert.Equal(t, "alice@work.com", id.Email)

	// ...but a different repo, with nothing configured, still falls through.
	other := insertTestRepository(t, db, "Other", "/tmp/o", userID)
	id2, err := svc.ResolveConfigured(ctx, userID, other)
	require.NoError(t, err)
	assert.True(t, id2.IsZero(), "unconfigured repo must fall through, got %+v", id2)
}

// The pinning pass must leave an unconfigured user's repo config untouched.
func TestSyncWorktreeIdentities_UnconfiguredUser_LeavesConfigAlone(t *testing.T) {
	db := openOrgTestDB(t)
	q := store.New(db)
	ctx := context.Background()
	userID := newOrgTestUser(t, db, "alice", "member")
	clearDisplayName(t, db, userID)
	svc := NewGitIdentityService(db)

	base := t.TempDir()
	wtPath := filepath.Join(base, "repo-a")
	initTestRepo(t, wtPath)
	repoID := insertTestRepository(t, db, "RepoA", wtPath, userID)
	wsID := insertTestWorkspace(t, db, userID, base)
	insertTestWorktree(t, db, wsID, repoID, wtPath)

	pinned, err := svc.SyncWorktreeIdentities(ctx, q, wsID, userID)
	require.NoError(t, err)
	assert.False(t, pinned)
	assert.Equal(t, "AmbientName", localConfig(t, wtPath, "user.name"),
		"must not overwrite the user's own git config with a synthetic identity")
}
