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
}

func NewGitOpsService(q *store.Queries, notifyHub *notify.NotificationHub) *GitOpsService {
	return &GitOpsService{q: q, notifyHub: notifyHub}
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
func (s *GitOpsService) Commit(ctx context.Context, workspaceID, repoID int64, message string) error {
	wsRepo, err := s.q.GetWorktreeByWorkspaceAndRepo(ctx, store.GetWorktreeByWorkspaceAndRepoParams{
		WorkspaceID:  workspaceID,
		RepositoryID: repoID,
	})
	if err != nil {
		return fmt.Errorf("get workspace repo: %w", err)
	}

	if err := git.CommitAll(wsRepo.WorktreePath, message); err != nil {
		return err
	}

	// Drop the sidebar's cached git badges for this worktree so the lazy
	// sidebar-git endpoint recomputes immediately instead of serving the
	// pre-commit counts until the TTL expires.
	sidebarGitCache.Invalidate(wsRepo.WorktreePath)
	if s.notifyHub != nil {
		s.notifyHub.Broadcast(notify.Notification{Topic: notify.TopicDiff, Action: "changed", ID: workspaceID})
		s.notifyHub.Broadcast(notify.Notification{Topic: notify.TopicGitStatus, Action: "changed", ID: workspaceID})
	}

	return nil
}

// Pull fetches and merges changes from the remote repository.
func (s *GitOpsService) Pull(ctx context.Context, workspaceID, repoID int64) error {
	wsRepo, err := s.q.GetWorktreeByWorkspaceAndRepo(ctx, store.GetWorktreeByWorkspaceAndRepoParams{
		WorkspaceID:  workspaceID,
		RepositoryID: repoID,
	})
	if err != nil {
		return fmt.Errorf("get workspace repo: %w", err)
	}

	if err := git.Pull(wsRepo.WorktreePath); err != nil {
		return err
	}

	sidebarGitCache.Invalidate(wsRepo.WorktreePath)
	if s.notifyHub != nil {
		s.notifyHub.Broadcast(notify.Notification{Topic: notify.TopicDiff, Action: "changed", ID: workspaceID})
		s.notifyHub.Broadcast(notify.Notification{Topic: notify.TopicGitStatus, Action: "changed", ID: workspaceID})
	}

	return nil
}

// Push pushes local changes to the remote repository.
func (s *GitOpsService) Push(ctx context.Context, workspaceID, repoID int64) error {
	wsRepo, err := s.q.GetWorktreeByWorkspaceAndRepo(ctx, store.GetWorktreeByWorkspaceAndRepoParams{
		WorkspaceID:  workspaceID,
		RepositoryID: repoID,
	})
	if err != nil {
		return fmt.Errorf("get workspace repo: %w", err)
	}

	if err := git.Push(wsRepo.WorktreePath); err != nil {
		return err
	}

	sidebarGitCache.Invalidate(wsRepo.WorktreePath)
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
