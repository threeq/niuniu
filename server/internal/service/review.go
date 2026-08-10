package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/niuniu-dev/niuniu/internal/git"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// RepoDiff is one repository's segment of a workspace "vs baseline" diff. It
// carries the resolved repository_id and worktree_path directly so the client
// never has to re-resolve a repo by name — every group the diff returns is
// independently actionable (open its line-level diff, jump to the repo page).
// RepositoryID is 0 when the worktree's source repo could not be matched to any
// registered repository (the group still lists its file changes).
type RepoDiff struct {
	Name         string         `json:"name"`
	RepositoryID int64          `json:"repository_id"`
	WorktreePath string         `json:"worktree_path"`
	BaseBranch   string         `json:"base_branch"`
	// CurrentBranch is the worktree's own checked-out branch (HEAD). Sourced
	// from the worktree row directly so it is present even for stale/orphan
	// associations (the sidebar's repo JOIN would drop those).
	CurrentBranch string `json:"current_branch"`
	// CloneURL is the registered repository's git remote, or "" when the repo
	// has no remote (or is unresolved). The desktop local-runner reads it to
	// seed-clone an empty bound directory (#478); the SPA ignores it.
	CloneURL string         `json:"clone_url"`
	Files    []git.FileDiff `json:"files"`
}

type ReviewService struct {
	q *store.Queries
	// agentProxy delivers review comments to the workspace's chat agent. Wired
	// via SetAgentProxy after construction; may be nil in tests that don't send.
	agentProxy AgentProxyShim
}

func NewReviewService(q *store.Queries) *ReviewService {
	return &ReviewService{q: q}
}

// SetAgentProxy wires the agentproxy so review comments reach the workspace's
// chat session (the same path the chat panel uses) rather than the PTY agent.
func (s *ReviewService) SetAgentProxy(p AgentProxyShim) {
	s.agentProxy = p
}

// GetDiff returns the diff for all repositories in a workspace, summary-only for
// resolved repos (no per-file raw_patch) — the shape the SPA list view wants.
func (s *ReviewService) GetDiff(ctx context.Context, workspaceID int64) ([]RepoDiff, error) {
	return s.getDiff(ctx, workspaceID, false)
}

// GetDiffWithPatch is GetDiff but keeps every file's full raw_patch, even for
// resolved repos. The local-runner sync (#472) consumes these patches to mirror
// the remote worktree into the bound directory, so it MUST NOT get the
// summary-only shape (which would make sync see "no changes" and apply nothing).
func (s *ReviewService) GetDiffWithPatch(ctx context.Context, workspaceID int64) ([]RepoDiff, error) {
	return s.getDiff(ctx, workspaceID, true)
}

// getDiff builds the per-worktree diff list. keepPatch retains each file's
// raw_patch (and hunks); when false, resolved repos are summarised to keep the
// list-view response small (the SPA re-fetches line-level data per repo lazily).
func (s *ReviewService) getDiff(ctx context.Context, workspaceID int64, keepPatch bool) ([]RepoDiff, error) {
	// Get all worktrees for the workspace.
	worktrees, err := s.q.ListWorktrees(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list workspace repos: %w", err)
	}

	// Reverse-lookup match set, loaded lazily — only when a worktree's stored
	// association turns out to be stale. Scoped to the workspace's OWN owner: a
	// workspace's worktrees only ever belong to repositories of the same owner,
	// and matching across owners would leak foreign repo ids (multi-tenant).
	var ownerRepos []store.Repository
	repos := func() []store.Repository {
		if ownerRepos == nil {
			ownerRepos = s.workspaceOwnerRepos(ctx, workspaceID)
		}
		return ownerRepos
	}

	result := make([]RepoDiff, 0, len(worktrees))
	for _, wt := range worktrees {
		name := ""
		repoID := wt.RepositoryID
		base := wt.BaseBranch
		cloneURL := ""
		stale := false

		if repo, err := s.q.GetRepository(ctx, wt.RepositoryID); err == nil {
			name = repo.Name
			cloneURL = repo.GitRemote.String
		} else if matched, ok := matchRepoByWorktree(wt.WorktreePath, repos()); ok {
			// Stored association is stale (source repo deleted/re-added under a new
			// id). Reverse-resolve the worktree's real source repo from its
			// directory and bind to the registered repository that shares it.
			name = matched.Name
			repoID = matched.ID
			cloneURL = matched.GitRemote.String
			stale = true
		} else {
			// No registered repository matches — still surface the worktree's
			// changes; the directory name is the best label, repoID 0 disables the
			// repo-scoped actions (jump / line-level diff) for this group only.
			name = filepath.Base(wt.WorktreePath)
			repoID = 0
			stale = true
		}

		// When the association was stale, the recorded base branch may be unreliable
		// too — and git.Diff would then silently collapse to "uncommitted only",
		// under-reporting the real change magnitude. Resolve a real base (preferring
		// the recorded one, then main/master) so the diff is measured against the
		// mainline, and report the base we actually used.
		if stale {
			base = git.ResolveDiffBase(wt.WorktreePath, wt.BaseBranch)
		}

		// Full "vs baseline" diff for this worktree. Runs git inside the worktree,
		// so it works regardless of whether the DB association is intact. A single
		// broken worktree must not blank the whole panel — surface the group with
		// no files rather than failing the entire request (resilience is the point
		// of the stale/orphan handling above).
		diffs, err := worktreeDiff(wt.WorktreePath, base)
		if err != nil {
			slog.Warn("workspace diff: worktree diff failed", "workspace", workspaceID, "worktree", wt.WorktreePath, "error", err)
			diffs = []git.FileDiff{}
		}

		// Line-level data (hunks + raw_patch) is only consumed inline for
		// unresolved groups (repoID 0), which the client cannot re-fetch by id.
		// Resolved groups lazily re-fetch via GetRepoDiff, so ship them
		// summary-only — otherwise every file's full unified diff rides on the
		// list response (10–100× the bytes the list view actually reads). The
		// runner sync path opts into full patches via keepPatch (it needs them to
		// mirror the worktree).
		if repoID > 0 && !keepPatch {
			diffs = summariseDiffs(diffs)
		}

		result = append(result, RepoDiff{
			Name:          name,
			RepositoryID:  repoID,
			WorktreePath:  wt.WorktreePath,
			BaseBranch:    base,
			CurrentBranch: wt.Branch,
			CloneURL:      cloneURL,
			Files:         diffs,
		})
	}

	return result, nil
}

// workspaceOwnerRepos returns the repositories owned by the workspace's owner —
// the only valid match set for reverse-resolving a stale worktree association.
// Errors degrade to an empty set (reverse lookup then simply finds no match).
func (s *ReviewService) workspaceOwnerRepos(ctx context.Context, workspaceID int64) []store.Repository {
	ws, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return []store.Repository{}
	}
	params := store.ListRepositoriesForOwnersParams{}
	if ws.OwnerType == "org" {
		params.OrgIds = []int64{ws.OwnerID}
	} else {
		params.OwnerID = ws.OwnerID
	}
	repos, err := s.q.ListRepositoriesForOwners(ctx, params)
	if err != nil {
		return []store.Repository{}
	}
	return repos
}

// GetRepoDiff returns the diff for a specific repository in a workspace.
func (s *ReviewService) GetRepoDiff(ctx context.Context, workspaceID, repoID int64) ([]git.FileDiff, error) {
	// Direct association first.
	wt, err := s.q.GetWorktreeByWorkspaceAndRepo(ctx, store.GetWorktreeByWorkspaceAndRepoParams{
		WorkspaceID:  workspaceID,
		RepositoryID: repoID,
	})
	if err != nil {
		// No (workspace, repo) row — the diff list may have reverse-resolved this
		// worktree to repoID from its directory rather than from the stored
		// association. Find the workspace worktree whose source repo matches repoID.
		wt, err = s.findWorktreeBySourceRepo(ctx, workspaceID, repoID)
		if err != nil {
			return nil, fmt.Errorf("get workspace repo: %w", err)
		}
	}

	return worktreeDiff(wt.WorktreePath, wt.BaseBranch)
}

// repoMatchesSource reports whether a registered repository is the source repo
// behind a worktree, comparing the origin remote first, then the on-disk source
// path. Empty identities never match: the remote check requires a non-empty URL
// on both sides, and SamePath rejects empty paths.
func repoMatchesSource(repo store.Repository, src git.WorktreeSource) bool {
	if src.RemoteURL != "" && repo.GitRemote.Valid && repo.GitRemote.String == src.RemoteURL {
		return true
	}
	return git.SamePath(repo.Path, src.SourcePath)
}

// matchRepoByWorktree reverse-resolves a worktree directory to the registered
// repository that shares its source identity (see repoMatchesSource).
func matchRepoByWorktree(worktreePath string, repos []store.Repository) (store.Repository, bool) {
	src := git.ResolveWorktreeSource(worktreePath)
	for _, r := range repos {
		if repoMatchesSource(r, src) {
			return r, true
		}
	}
	return store.Repository{}, false
}

// findWorktreeBySourceRepo locates the workspace worktree whose on-disk source
// repo matches repoID — the inverse of matchRepoByWorktree, used when a group's
// repository_id was reverse-resolved and so has no direct (workspace, repo) row.
func (s *ReviewService) findWorktreeBySourceRepo(ctx context.Context, workspaceID, repoID int64) (store.Worktree, error) {
	repo, err := s.q.GetRepository(ctx, repoID)
	if err != nil {
		return store.Worktree{}, err
	}
	worktrees, err := s.q.ListWorktrees(ctx, workspaceID)
	if err != nil {
		return store.Worktree{}, err
	}
	for _, wt := range worktrees {
		if repoMatchesSource(repo, git.ResolveWorktreeSource(wt.WorktreePath)) {
			return wt, nil
		}
	}
	return store.Worktree{}, fmt.Errorf("no worktree in workspace %d matches repository %d source", workspaceID, repoID)
}

// summariseDiffs strips the line-level payload (hunks + raw_patch) from a file
// list, leaving only the summary fields the grouped list view renders.
func summariseDiffs(files []git.FileDiff) []git.FileDiff {
	out := make([]git.FileDiff, len(files))
	for i, f := range files {
		f.Hunks = nil
		f.RawPatch = ""
		out[i] = f
	}
	return out
}

// worktreeDiff returns the full "vs baseline" file list for a worktree: the
// committed+uncommitted tracked diff against base, with untracked files merged
// in (base...HEAD does not include untracked files).
func worktreeDiff(worktreePath, base string) ([]git.FileDiff, error) {
	diffs, err := git.Diff(worktreePath, base)
	if err != nil {
		return nil, err
	}
	untracked, err := git.UntrackedDiffs(worktreePath)
	if err != nil {
		return nil, err
	}
	return append(diffs, untracked...), nil
}

// ListComments returns all comments for a workspace.
func (s *ReviewService) ListComments(ctx context.Context, workspaceID int64) ([]store.Comment, error) {
	return s.q.ListCommentsByWorkspace(ctx, workspaceID)
}

// CreateCommentInput holds the data for creating a comment.
type CreateCommentInput struct {
	Repo       string
	FilePath   string
	LineNumber *int
	Content    string
}

// CreateComment creates a new comment for a workspace.
func (s *ReviewService) CreateComment(ctx context.Context, workspaceID int64, input CreateCommentInput) (store.Comment, error) {
	var lineNumber sql.NullInt64
	if input.LineNumber != nil {
		lineNumber = sql.NullInt64{Int64: int64(*input.LineNumber), Valid: true}
	}

	return s.q.CreateComment(ctx, store.CreateCommentParams{
		WorkspaceID: workspaceID,
		Repo:        input.Repo,
		FilePath:    input.FilePath,
		LineNumber:  lineNumber,
		Content:     input.Content,
	})
}

// SendCommentToAgent delivers a review comment to the workspace's chat agent as
// feedback. It routes through the agentproxy session (the same path the chat
// panel uses) — starting a session if none is live — instead of the PTY
// AgentManager, which is not running during a proxy-based chat and produced the
// "no agent running for workspace N" error when sending comments in focus mode.
// CommentWorkspaceID resolves the workspace a comment belongs to, so handlers
// can authorize the caller against that workspace before acting on the comment
// (the /comments/:id routes carry no workspace in the path).
func (s *ReviewService) CommentWorkspaceID(ctx context.Context, commentID int64) (int64, error) {
	comment, err := s.q.GetComment(ctx, commentID)
	if err != nil {
		return 0, err
	}
	return comment.WorkspaceID, nil
}

func (s *ReviewService) SendCommentToAgent(ctx context.Context, commentID int64) error {
	if s.agentProxy == nil {
		return fmt.Errorf("agent proxy not configured")
	}

	// Get comment from DB
	comment, err := s.q.GetComment(ctx, commentID)
	if err != nil {
		return fmt.Errorf("get comment: %w", err)
	}

	// Resolve the workspace path: the proxy needs the worktree dir to run the CLI.
	ws, err := s.q.GetWorkspace(ctx, comment.WorkspaceID)
	if err != nil {
		return fmt.Errorf("get workspace: %w", err)
	}

	// Format as instruction
	var lineInfo string
	if comment.LineNumber.Valid {
		lineInfo = fmt.Sprintf(":%d", comment.LineNumber.Int64)
	}
	location := comment.FilePath
	if comment.Repo != "" {
		location = fmt.Sprintf("%s › %s", comment.Repo, comment.FilePath)
	}
	instruction := fmt.Sprintf("Review comment on %s%s: %s", location, lineInfo, comment.Content)

	// A review comment is an explicit manual user send: open the Enqueue gate in
	// case an autohost resume is holding it closed, then deliver (queues if a
	// loop is live, otherwise starts one).
	s.agentProxy.PrepareUserSend(ctx, comment.WorkspaceID)
	if _, _, err := s.agentProxy.Deliver(ctx, comment.WorkspaceID, ws.Path, instruction, ""); err != nil {
		return fmt.Errorf("send to agent: %w", err)
	}

	// Mark as sent
	return s.q.MarkCommentSent(ctx, commentID)
}
