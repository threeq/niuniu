package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/git"
	"github.com/niuniu-dev/niuniu/internal/store"
)

type WorktreeService struct {
	q   *store.Queries
	cfg *config.WorkspaceConfig
}

func NewWorktreeService(q *store.Queries, cfg *config.WorkspaceConfig) *WorktreeService {
	return &WorktreeService{q: q, cfg: cfg}
}

type CreateWorktreeInput struct {
	WorkspaceID  int64
	RepositoryID int64
	Branch       string
}

type WorktreeResult struct {
	ID           int64
	WorkspaceID  int64
	RepositoryID int64
	WorktreePath string
	Branch       string
}

func (s *WorktreeService) List(ctx context.Context, workspaceID, repoID int64) ([]store.Worktree, error) {
	switch {
	case workspaceID > 0 && repoID > 0:
		return s.q.ListWorktreesByWorkspaceAndRepo(ctx, store.ListWorktreesByWorkspaceAndRepoParams{
			WorkspaceID:  workspaceID,
			RepositoryID: repoID,
		})
	case workspaceID > 0:
		return s.q.ListWorktrees(ctx, workspaceID)
	case repoID > 0:
		return s.q.ListWorktreesByRepository(ctx, repoID)
	default:
		return s.q.ListAllWorktrees(ctx)
	}
}

func (s *WorktreeService) Create(ctx context.Context, input CreateWorktreeInput) (*WorktreeResult, error) {
	// Get workspace
	ws, err := s.q.GetWorkspace(ctx, input.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("workspace not found: %w", err)
	}

	// Get repository
	repo, err := s.q.GetRepository(ctx, input.RepositoryID)
	if err != nil {
		return nil, fmt.Errorf("repository not found: %w", err)
	}

	// Determine branch — request value takes precedence, then repo's default,
	// then the repo's actual HEAD. Don't fall back to a hardcoded "main" since
	// many repos use master/develop/etc. and a missing ref makes git fail.
	branch, err := resolveBaseBranch(repo, input.Branch)
	if err != nil {
		return nil, fmt.Errorf("resolve base branch: %w", err)
	}

	// Generate worktree path
	worktreePath := GenerateWorktreePath(ws.Path, input.RepositoryID, repo.Name, branch)

	// Check if worktree already exists
	if _, err := os.Stat(worktreePath); err == nil {
		return nil, fmt.Errorf("worktree path already exists: %s", worktreePath)
	}

	// Git branch name with workspace prefix: ws-<id>/<branch>
	wtBranch := GenerateWorktreeBranch(input.WorkspaceID, branch)

	// Create git worktree forked from the user-specified base branch.
	if err := git.WorktreeAdd(repo.Path, worktreePath, wtBranch, branch); err != nil {
		return nil, fmt.Errorf("git worktree add: %w", err)
	}

	// Create DB record
	wsRepo, err := s.q.CreateWorktree(ctx, store.CreateWorktreeParams{
		WorkspaceID:  input.WorkspaceID,
		RepositoryID: input.RepositoryID,
		WorktreePath: worktreePath,
		Branch:       wtBranch,
		BaseBranch:   branch,
	})
	if err != nil {
		git.WorktreeRemove(repo.Path, worktreePath)
		return nil, fmt.Errorf("create workspace repo: %w", err)
	}

	return &WorktreeResult{
		ID:           wsRepo.ID,
		WorkspaceID:  wsRepo.WorkspaceID,
		RepositoryID: wsRepo.RepositoryID,
		WorktreePath: wsRepo.WorktreePath,
		Branch:       wsRepo.Branch,
	}, nil
}

func (s *WorktreeService) Remove(ctx context.Context, id int64) error {
	wsRepo, err := s.q.GetWorktree(ctx, id)
	if err != nil {
		return fmt.Errorf("worktree not found: %w", err)
	}

	repo, err := s.q.GetRepository(ctx, wsRepo.RepositoryID)
	if err != nil {
		return fmt.Errorf("repository not found: %w", err)
	}

	if err := git.WorktreeRemove(repo.Path, wsRepo.WorktreePath); err != nil {
		return fmt.Errorf("git worktree remove: %w", err)
	}

	return s.q.DeleteWorktree(ctx, id)
}

func (s *WorktreeService) GetTree(ctx context.Context, worktreePath string) (*TreeResult, error) {
	return buildTree(worktreePath)
}

func (s *WorktreeService) GetStatus(ctx context.Context, id int64) (*GitStatusResult, error) {
	worktree, err := s.q.GetWorktree(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("worktree not found: %w", err)
	}

	statuses, err := git.Status(worktree.WorktreePath)
	if err != nil {
		return nil, fmt.Errorf("git status: %w", err)
	}

	// Transform to categorized result
	result := &GitStatusResult{
		Modified:  []string{},
		Added:     []string{},
		Deleted:   []string{},
		Untracked: []string{},
	}

	for _, f := range statuses {
		switch f.Status {
		case "??":
			result.Untracked = append(result.Untracked, f.Path)
		case "A", "AM":
			result.Added = append(result.Added, f.Path)
		case "D":
			result.Deleted = append(result.Deleted, f.Path)
		default: // M, MM, T, R, etc.
			result.Modified = append(result.Modified, f.Path)
		}
	}

	return result, nil
}

type GitStatusResult struct {
	Modified  []string `json:"modified"`
	Added     []string `json:"added"`
	Deleted   []string `json:"deleted"`
	Untracked []string `json:"untracked"`
	// Broken is true when the worktree directory exists on disk but its
	// gitlink can't be resolved (typically because the parent repo was
	// moved or deleted). The list fields will be empty. Reason is a
	// short, sanitized human-readable string — the raw git stderr is NOT
	// returned because it leaks data-dir paths across tenants in team
	// edition.
	Broken bool   `json:"broken,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// hasDanglingGitlink returns true when the worktree's `.git` file is a
// gitlink whose target directory does not exist. This deterministically
// identifies the "structurally broken worktree" class (parent repo moved or
// deleted) without relying on git's stderr text — which `git.Status` discards
// because it uses cmd.Output(). Anything else (missing .git, full repo,
// readable but git failed for transient/IO reasons) returns false so the
// caller surfaces a 5xx instead of permanently masking the failure behind a
// "broken" badge.
func hasDanglingGitlink(worktreePath string) bool {
	gitFile := filepath.Join(worktreePath, ".git")
	info, err := os.Stat(gitFile)
	if err != nil || info.IsDir() {
		return false
	}
	data, err := os.ReadFile(gitFile)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "gitdir:"); ok {
			target := strings.TrimSpace(rest)
			if target == "" {
				return false
			}
			if _, err := os.Stat(target); os.IsNotExist(err) {
				return true
			}
			return false
		}
	}
	return false
}

// resolveBaseBranch picks the branch a new workspace worktree should fork from.
// Order: requested → repo.default_branch → repo's actual HEAD branch.
// Returns an error only when the request was empty AND repo has no default branch
// AND HEAD is detached — caller surfaces that to the user instead of guessing.
func resolveBaseBranch(repo store.Repository, requested string) (string, error) {
	if requested != "" {
		return requested, nil
	}
	if repo.DefaultBranch.Valid && repo.DefaultBranch.String != "" {
		return repo.DefaultBranch.String, nil
	}
	// git.CurrentBranch wraps `rev-parse --abbrev-ref HEAD`, which returns the
	// literal string "HEAD" when the repo is in detached state. Treat that as
	// "no resolvable branch" so the caller surfaces a clear error instead of
	// trying to fork from a ref named HEAD.
	head, err := git.CurrentBranch(repo.Path)
	if err == nil && head != "" && head != "HEAD" {
		return head, nil
	}
	if err == nil {
		return "", fmt.Errorf("branch unspecified, repo has no default_branch, and HEAD is detached")
	}
	return "", fmt.Errorf("branch unspecified, repo has no default_branch, and HEAD is detached: %w", err)
}

// GenerateWorktreeBranch generates a branch name with workspace prefix: ws-<wsId>/<branch>
func GenerateWorktreeBranch(workspaceID int64, branch string) string {
	return fmt.Sprintf("ws-%d/%s", workspaceID, branch)
}

// GenerateWorktreePath generates path in format: {wsPath}/.worktrees/{repoName}-{safeBranch}
// repoName is provided by the caller (human-readable repository name).
// Falls back to wt-{repoId} if repoName is empty.
func GenerateWorktreePath(wsPath string, repoID int64, repoName, branch string) string {
	safeBranch := sanitizeBranch(branch)
	prefix := repoName
	if prefix == "" {
		prefix = fmt.Sprintf("wt-%d", repoID)
	}
	dirName := sanitizeBranch(prefix) + "-" + safeBranch
	return filepath.Join(wsPath, ".worktrees", dirName)
}

func sanitizeBranch(name string) string {
	replacer := strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "*", "_", "?", "_",
		"\"", "_", "<", "_", ">", "_", "|", "_",
	)
	result := replacer.Replace(name)
	if len(result) > 128 {
		h := sha256.Sum256([]byte(name))
		result = result[:120] + "_" + hex.EncodeToString(h[:4])
	}
	return result
}

type TreeResult struct {
	Path  string     `json:"path"`
	Items []TreeItem `json:"items"`
}

type TreeItem struct {
	Name string `json:"name"`
	Path string `json:"path"` // relative path for lazy-loading subdirectories
	Type string `json:"type"` // "dir" or "file"
	Size int64  `json:"size"`
}

func buildTree(dirPath string) (*TreeResult, error) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, err
	}
	items := make([]TreeItem, 0, len(entries))
	for _, entry := range entries {
		info, _ := entry.Info()
		items = append(items, TreeItem{
			Name: entry.Name(),
			Path: entry.Name(),
			Type: map[bool]string{true: "dir", false: "file"}[entry.IsDir()],
			Size: info.Size(),
		})
	}
	return &TreeResult{Path: dirPath, Items: items}, nil
}
