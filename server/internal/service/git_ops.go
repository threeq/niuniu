package service

import (
	"context"
	"fmt"

	"github.com/niuniu-dev/niuniu/internal/git"
	"github.com/niuniu-dev/niuniu/internal/notify"
	"github.com/niuniu-dev/niuniu/internal/store"
)

type GitOpsService struct {
	q         *store.Queries
	notifyHub *notify.NotificationHub
	// gitIdentity resolves the niuniu user's author signature for a commit.
	// Nil-safe: when unset, commits fall through to repo-local then OS-global
	// git config, which is the pre-per-user-identity behavior.
	gitIdentity *GitIdentityService
	// gitRemoteCred resolves the caller's stored git credential so workspace
	// pull/push authenticate the same way the repository panel does. Nil-safe:
	// when unset, remotes fall back to bare git (public remotes unaffected).
	gitRemoteCred *GitRemoteCredentialService
}

func NewGitOpsService(q *store.Queries, notifyHub *notify.NotificationHub) *GitOpsService {
	return &GitOpsService{q: q, notifyHub: notifyHub}
}

// WithGitIdentity attaches the identity resolver so server-initiated commits
// are attributed to the acting niuniu user rather than whatever OS-global
// git config the daemon happens to run under.
func (s *GitOpsService) WithGitIdentity(gi *GitIdentityService) *GitOpsService {
	s.gitIdentity = gi
	return s
}

// Status returns the git status for a repository in a workspace.
func (s *GitOpsService) Status(ctx context.Context, workspaceID, repoID int64) ([]git.FileStatus, error) {
	wsRepo, err := s.q.GetWorktreeByWorkspaceAndRepo(ctx, store.GetWorktreeByWorkspaceAndRepoParams{
		WorkspaceID:  workspaceID,
		RepositoryID: repoID,
	})
	if err != nil {
		return nil, fmt.Errorf("get workspace repo: %w", err)
	}

	return git.Status(wsRepo.WorktreePath)
}

// Log returns the commit history for a repository in a workspace.
func (s *GitOpsService) Log(ctx context.Context, workspaceID, repoID int64, limit int) ([]git.LogEntry, error) {
	wsRepo, err := s.q.GetWorktreeByWorkspaceAndRepo(ctx, store.GetWorktreeByWorkspaceAndRepoParams{
		WorkspaceID:  workspaceID,
		RepositoryID: repoID,
	})
	if err != nil {
		return nil, fmt.Errorf("get workspace repo: %w", err)
	}

	return git.Log(wsRepo.WorktreePath, limit)
}

// Commit creates a commit with all staged changes in a repository.
//
// userID is the acting niuniu user; it drives author attribution through the
// precedence chain in GitIdentityService.ResolveForRepository (per-(user,repo)
// override → user global → synthetic fallback). Pass 0 for callers with no
// authenticated user — the commit then falls through to repo-local then
// OS-global git config, matching legacy behavior.
func (s *GitOpsService) Commit(ctx context.Context, workspaceID, repoID int64, message string, userID int64) error {
	wsRepo, err := s.q.GetWorktreeByWorkspaceAndRepo(ctx, store.GetWorktreeByWorkspaceAndRepoParams{
		WorkspaceID:  workspaceID,
		RepositoryID: repoID,
	})
	if err != nil {
		return fmt.Errorf("get workspace repo: %w", err)
	}

	// Resolve per-(user, repository) identity. repoID is the niuniu
	// repository row, which is exactly the override key — so a user who set a
	// different signature for this one repo gets it honored here.
	// ResolveConfigured (not ResolveForRepository) so a user who configured
	// nothing falls through to their repo/global git config.
	var id git.Identity
	if s.gitIdentity != nil && userID > 0 {
		id, err = s.gitIdentity.ResolveConfigured(ctx, userID, repoID)
		if err != nil {
			return fmt.Errorf("resolve git identity: %w", err)
		}
	}

	if err := git.CommitAs(ctx, wsRepo.WorktreePath, id, message, true); err != nil {
		return err
	}

	// Drop the sidebar's cached git badges for this worktree so the lazy
	// sidebar-git endpoint recomputes immediately instead of serving the
	// pre-commit counts until the TTL expires.
	sidebarGitCache.Invalidate(wsRepo.WorktreePath)
	workspaceDiffCache.InvalidateWorkspace(workspaceID)
	if s.notifyHub != nil {
		s.notifyHub.Broadcast(notify.Notification{Topic: notify.TopicDiff, Action: "changed", ID: workspaceID})
		s.notifyHub.Broadcast(notify.Notification{Topic: notify.TopicGitStatus, Action: "changed", ID: workspaceID})
	}

	return nil
}

// Pull fetches and merges changes from the remote repository.
func (s *GitOpsService) Pull(ctx context.Context, userID int64, workspaceID, repoID int64) error {
	wsRepo, err := s.q.GetWorktreeByWorkspaceAndRepo(ctx, store.GetWorktreeByWorkspaceAndRepoParams{
		WorkspaceID:  workspaceID,
		RepositoryID: repoID,
	})
	if err != nil {
		return fmt.Errorf("get workspace repo: %w", err)
	}

	remote := remoteForRepoID(ctx, s.q, repoID)
	if err := guardRemoteEgressWith(ctx, s.gitRemoteCred, userID, remote); err != nil {
		return err
	}
	env, cleanup := remoteAuthEnv(ctx, s.gitRemoteCred, userID, remote)
	defer cleanup()

	if err := git.PullEnv(wsRepo.WorktreePath, env); err != nil {
		return err
	}

	sidebarGitCache.Invalidate(wsRepo.WorktreePath)
	workspaceDiffCache.InvalidateWorkspace(workspaceID)
	if s.notifyHub != nil {
		s.notifyHub.Broadcast(notify.Notification{Topic: notify.TopicDiff, Action: "changed", ID: workspaceID})
		s.notifyHub.Broadcast(notify.Notification{Topic: notify.TopicGitStatus, Action: "changed", ID: workspaceID})
	}

	return nil
}

// Push pushes local changes to the remote repository.
func (s *GitOpsService) Push(ctx context.Context, userID int64, workspaceID, repoID int64) error {
	wsRepo, err := s.q.GetWorktreeByWorkspaceAndRepo(ctx, store.GetWorktreeByWorkspaceAndRepoParams{
		WorkspaceID:  workspaceID,
		RepositoryID: repoID,
	})
	if err != nil {
		return fmt.Errorf("get workspace repo: %w", err)
	}

	remote := remoteForRepoID(ctx, s.q, repoID)
	if err := guardRemoteEgressWith(ctx, s.gitRemoteCred, userID, remote); err != nil {
		return err
	}
	env, cleanup := remoteAuthEnv(ctx, s.gitRemoteCred, userID, remote)
	defer cleanup()

	if err := git.PushEnv(wsRepo.WorktreePath, env); err != nil {
		return err
	}

	sidebarGitCache.Invalidate(wsRepo.WorktreePath)
	workspaceDiffCache.InvalidateWorkspace(workspaceID)
	if s.notifyHub != nil {
		s.notifyHub.Broadcast(notify.Notification{Topic: notify.TopicDiff, Action: "changed", ID: workspaceID})
		s.notifyHub.Broadcast(notify.Notification{Topic: notify.TopicGitStatus, Action: "changed", ID: workspaceID})
	}

	return nil
}

// Fetch fetches remote branches without merging.
func (s *GitOpsService) Fetch(ctx context.Context, workspaceID, repoID int64) error {
	wsRepo, err := s.q.GetWorktreeByWorkspaceAndRepo(ctx, store.GetWorktreeByWorkspaceAndRepoParams{
		WorkspaceID:  workspaceID,
		RepositoryID: repoID,
	})
	if err != nil {
		return fmt.Errorf("get workspace repo: %w", err)
	}

	return git.Fetch(wsRepo.WorktreePath)
}

// Branches returns all branches and the current branch for a repository.
func (s *GitOpsService) Branches(ctx context.Context, workspaceID, repoID int64) ([]string, string, error) {
	wsRepo, err := s.q.GetWorktreeByWorkspaceAndRepo(ctx, store.GetWorktreeByWorkspaceAndRepoParams{
		WorkspaceID:  workspaceID,
		RepositoryID: repoID,
	})
	if err != nil {
		return nil, "", fmt.Errorf("get workspace repo: %w", err)
	}

	branches, err := git.ListBranches(wsRepo.WorktreePath)
	if err != nil {
		return nil, "", err
	}

	currentBranch, err := git.CurrentBranch(wsRepo.WorktreePath)
	if err != nil {
		return nil, "", err
	}

	return branches, currentBranch, nil
}

// GetFileContent returns the file content from HEAD.
func (s *GitOpsService) GetFileContent(ctx context.Context, workspaceID, repoID int64, path string) (string, error) {
	wsRepo, err := s.q.GetWorktreeByWorkspaceAndRepo(ctx, store.GetWorktreeByWorkspaceAndRepoParams{
		WorkspaceID:  workspaceID,
		RepositoryID: repoID,
	})
	if err != nil {
		return "", fmt.Errorf("get workspace repo: %w", err)
	}
	return git.FileContent(wsRepo.WorktreePath, path)
}

// GetFileDiff returns the diff for a single file.
func (s *GitOpsService) GetFileDiff(ctx context.Context, workspaceID, repoID int64, path string) (*git.FileDiff, error) {
	wsRepo, err := s.q.GetWorktreeByWorkspaceAndRepo(ctx, store.GetWorktreeByWorkspaceAndRepoParams{
		WorkspaceID:  workspaceID,
		RepositoryID: repoID,
	})
	if err != nil {
		return nil, fmt.Errorf("get workspace repo: %w", err)
	}
	return git.DiffFile(wsRepo.WorktreePath, wsRepo.BaseBranch, path)
}

// StatusByRepoID returns the git status for a repository.
// If workspaceID > 0, uses the worktree path for that workspace; otherwise uses the repository's original path.
func (s *GitOpsService) StatusByRepoID(ctx context.Context, repoID, workspaceID int64) ([]git.FileStatus, error) {
	repo, err := s.q.GetRepository(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("get repository: %w", err)
	}

	// If workspaceID is provided, use the worktree path for that workspace
	if workspaceID > 0 {
		wsRepo, err := s.q.GetWorktreeByWorkspaceAndRepo(ctx, store.GetWorktreeByWorkspaceAndRepoParams{
			WorkspaceID:  workspaceID,
			RepositoryID: repoID,
		})
		if err != nil {
			return nil, fmt.Errorf("get workspace repo: %w", err)
		}
		return git.Status(wsRepo.WorktreePath)
	}

	// Otherwise use the repository's original path
	return git.Status(repo.Path)
}

// remoteForRepoID loads the repository's configured git remote for a
// workspace-level pull/push. Empty when the row is gone (git then reports the
// missing remote itself); []string-free by design so callers stay simple.
func remoteForRepoID(ctx context.Context, q *store.Queries, repoID int64) string {
	repo, err := q.GetRepository(ctx, repoID)
	if err != nil {
		return ""
	}
	return repo.GitRemote.String
}

// WithGitRemoteCredentials attaches the per-user git credential store so
// workspace pull/push authenticate (HTTPS token via credential.helper, SSH key
// via GIT_SSH_COMMAND) and honour the same egress guard as the repo panel.
func (s *GitOpsService) WithGitRemoteCredentials(svc *GitRemoteCredentialService) *GitOpsService {
	s.gitRemoteCred = svc
	return s
}
