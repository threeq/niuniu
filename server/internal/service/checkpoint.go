package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/niuniu-dev/niuniu/internal/git"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// CheckpointService is the autohost 安全网: it snapshots a workspace's worktree(s)
// into hidden git refs (refs/niuniu/<workspace>/<issue>/<step>) at meaningful
// moments — entering the implement column, passing a gate, autohost finishing — so
// an issue gains a time dimension on top of niuniu's space isolation. From those
// snapshots it renders a per-step timeline, diffs any step, one-click reverts the
// worktree to a step (without losing later work — the refs survive), and, on a gate
// failure, rewinds to the last gate-passing checkpoint so autohost can resume from a
// known-good baseline instead of a whole-column rollback.
//
// The git objects/refs live in each repo worktree (see internal/git/checkpoint.go);
// this service owns the issue_checkpoints metadata table (migrate-only, raw SQL —
// see store/migrate.go migrateIssueCheckpoints) that indexes them for querying.
type CheckpointService struct {
	db *store.DB
	q  *store.Queries
	// stepMu serializes step-number allocation + insert across Snapshot calls so two
	// concurrent snapshots for the same issue cannot read the same MAX(step) and
	// collide on a step number. Snapshots are infrequent, so a single mutex is ample.
	stepMu sync.Mutex
}

// NewCheckpointService constructs the service. db/q are required.
func NewCheckpointService(db *store.DB, q *store.Queries) *CheckpointService {
	return &CheckpointService{db: db, q: q}
}

// Checkpoint kinds.
const (
	CheckpointKindAdvance       = "advance"        // entering the implement column
	CheckpointKindGatePass      = "gate_pass"      // a gate run passed
	CheckpointKindAutohostFinal = "autohost_final" // autohost 收尾 (finish) snapshot
	CheckpointKindManual        = "manual"         // user/agent-requested snapshot
	CheckpointGateStatusPass    = "pass"
	CheckpointGateStatusFail    = "fail"
)

// checkpointRepoTarget is one repo worktree to snapshot / revert.
type checkpointRepoTarget struct {
	repositoryID int64
	repoName     string
	worktreePath string
}

// CheckpointRepo is one repo's slice of a checkpoint step (a single git snapshot).
type CheckpointRepo struct {
	ID           int64  `json:"id"`
	RepositoryID int64  `json:"repository_id"`
	RepoName     string `json:"repo_name"`
	WorktreePath string `json:"worktree_path"`
	GitRef       string `json:"git_ref"`
	CommitHash   string `json:"commit_hash"`
	ParentHash   string `json:"parent_hash"`
}

// CheckpointStep is one point on an issue's checkpoint timeline: a step number, the
// trigger kind + gate status + label, its timestamp, and one entry per repo snapped.
type CheckpointStep struct {
	Step       int              `json:"step"`
	Kind       string           `json:"kind"`
	GateStatus string           `json:"gate_status"`
	Label      string           `json:"label"`
	CreatedAt  string           `json:"created_at"`
	Repos      []CheckpointRepo `json:"repos"`
}

// SnapshotResult reports what a Snapshot produced.
type SnapshotResult struct {
	Step  int              `json:"step"`
	Repos []CheckpointRepo `json:"repos"`
}

// Snapshot captures the current worktree state of every repo in the issue's active
// workspace as a new checkpoint step. Each repo's snapshot chains onto that repo's
// previous checkpoint (so the ref graph is a per-repo timeline) or HEAD when it is
// the first. Best-effort per repo: a worktree that cannot be snapped (not a git
// repo) is logged and skipped, never failing the whole call. Returns the assigned
// step and the per-repo snapshots; a step with zero snapped repos returns step 0.
func (s *CheckpointService) Snapshot(ctx context.Context, issueID, workspaceID int64, kind, label, gateStatus string) (SnapshotResult, error) {
	if s == nil || s.db == nil || s.q == nil {
		return SnapshotResult{}, nil
	}
	targets := s.repoTargets(ctx, workspaceID)
	if len(targets) == 0 {
		return SnapshotResult{}, nil
	}
	// Serialize step allocation + inserts so concurrent snapshots for the same issue
	// don't read an identical MAX(step) and merge into one timeline entry.
	s.stepMu.Lock()
	defer s.stepMu.Unlock()
	step := s.nextStep(ctx, issueID)
	wsStr := strconv.FormatInt(workspaceID, 10)
	issueStr := strconv.FormatInt(issueID, 10)
	msg := fmt.Sprintf("[%s step %d] %s", kind, step, strings.TrimSpace(label))

	var repos []CheckpointRepo
	for _, t := range targets {
		requested := s.lastCommitForRepo(ctx, issueID, t.repositoryID)
		ref := git.CheckpointRef(wsStr, issueStr, step)
		commit, actualParent, err := git.WriteCheckpoint(t.worktreePath, ref, requested, msg)
		if err != nil {
			slog.Warn("checkpoint: snapshot skipped (not a git worktree?)",
				"issueID", issueID, "repo", t.repoName, "path", t.worktreePath, "error", err)
			continue
		}
		// Persist the parent the commit was ACTUALLY built on (WriteCheckpoint falls
		// back to HEAD/parentless when the requested parent does not resolve), so the
		// stored metadata never diverges from the commit object.
		id, err := s.insertRow(ctx, issueID, workspaceID, t, step, kind, gateStatus, label, ref, commit, actualParent)
		if err != nil {
			slog.Warn("checkpoint: persist row failed", "issueID", issueID, "repo", t.repoName, "error", err)
			continue
		}
		repos = append(repos, CheckpointRepo{
			ID: id, RepositoryID: t.repositoryID, RepoName: t.repoName, WorktreePath: t.worktreePath,
			GitRef: ref, CommitHash: commit, ParentHash: actualParent,
		})
	}
	if len(repos) == 0 {
		return SnapshotResult{}, nil
	}
	slog.Info("checkpoint: snapshot", "issueID", issueID, "step", step, "kind", kind, "repos", len(repos))
	return SnapshotResult{Step: step, Repos: repos}, nil
}

// Timeline returns the issue's checkpoint steps in ascending order, each grouping
// its per-repo snapshots.
func (s *CheckpointService) Timeline(ctx context.Context, issueID int64) ([]CheckpointStep, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, repository_id, repo_name, worktree_path, git_ref, commit_hash, parent_hash,
		       step, kind, gate_status, label, created_at
		FROM issue_checkpoints WHERE issue_id = ? ORDER BY step ASC, id ASC`, issueID)
	if err != nil {
		return nil, fmt.Errorf("checkpoint timeline: %w", err)
	}
	defer rows.Close()

	byStep := map[int]*CheckpointStep{}
	var order []int
	for rows.Next() {
		var (
			id                  int64
			repoID              sql.NullInt64
			repoName, wtPath    string
			gitRef, commit, par string
			step                int
			kind, gate, label   string
			createdAt           sql.NullString
		)
		if err := rows.Scan(&id, &repoID, &repoName, &wtPath, &gitRef, &commit, &par,
			&step, &kind, &gate, &label, &createdAt); err != nil {
			return nil, fmt.Errorf("checkpoint timeline scan: %w", err)
		}
		st, ok := byStep[step]
		if !ok {
			st = &CheckpointStep{Step: step, Kind: kind, GateStatus: gate, Label: label, CreatedAt: createdAt.String}
			byStep[step] = st
			order = append(order, step)
		}
		st.Repos = append(st.Repos, CheckpointRepo{
			ID: id, RepositoryID: repoID.Int64, RepoName: repoName, WorktreePath: wtPath,
			GitRef: gitRef, CommitHash: commit, ParentHash: par,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]CheckpointStep, 0, len(order))
	for _, step := range order {
		out = append(out, *byStep[step])
	}
	return out, nil
}

// StepDiff returns the file-level diff a single checkpoint row introduced (its
// snapshot vs its parent — i.e. the change captured at that step for that repo).
// It diffs against the commit's OWN first parent (empty "from") rather than the
// stored parent_hash: the two agree in the normal case, but the commit's recorded
// parent is always resolvable in this repo, whereas a stored parent_hash could be
// stale (e.g. a workspace re-created for the same issue, whose earlier checkpoint
// commit lives in a different object store) and make git diff fail.
func (s *CheckpointService) StepDiff(ctx context.Context, checkpointID int64) ([]git.FileDiff, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("checkpoint diff: service unavailable")
	}
	var wtPath, commit string
	err := s.db.QueryRowContext(ctx,
		`SELECT worktree_path, commit_hash FROM issue_checkpoints WHERE id = ?`,
		checkpointID).Scan(&wtPath, &commit)
	if err != nil {
		return nil, fmt.Errorf("checkpoint diff: load row %d: %w", checkpointID, err)
	}
	return git.CheckpointDiff(wtPath, "", commit)
}

// RevertRepoResult reports the outcome of reverting one repo to a checkpoint.
type RevertRepoResult struct {
	RepositoryID int64  `json:"repository_id"`
	RepoName     string `json:"repo_name"`
	WorktreePath string `json:"worktree_path"`
	CommitHash   string `json:"commit_hash"`
	OK           bool   `json:"ok"`
	Error        string `json:"error,omitempty"`
}

// Revert restores every repo worktree to its snapshot at the given step. It never
// deletes checkpoint refs, so newer work is not destroyed — a later Revert forward
// re-applies it. Returns a per-repo result; an error only when the step is unknown.
func (s *CheckpointService) Revert(ctx context.Context, issueID int64, step int) ([]RevertRepoResult, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("checkpoint revert: service unavailable")
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT repository_id, repo_name, worktree_path, commit_hash FROM issue_checkpoints
		 WHERE issue_id = ? AND step = ? ORDER BY id ASC`, issueID, step)
	if err != nil {
		return nil, fmt.Errorf("checkpoint revert: load step: %w", err)
	}
	defer rows.Close()
	var results []RevertRepoResult
	for rows.Next() {
		var (
			repoID       sql.NullInt64
			repoName, wt string
			commit       string
		)
		if err := rows.Scan(&repoID, &repoName, &wt, &commit); err != nil {
			return nil, fmt.Errorf("checkpoint revert scan: %w", err)
		}
		res := RevertRepoResult{RepositoryID: repoID.Int64, RepoName: repoName, WorktreePath: wt, CommitHash: commit}
		if err := git.RevertToCheckpoint(wt, commit); err != nil {
			res.Error = err.Error()
			slog.Warn("checkpoint: revert failed", "issueID", issueID, "step", step, "repo", repoName, "error", err)
		} else {
			res.OK = true
		}
		results = append(results, res)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("checkpoint revert: no checkpoint at step %d for issue %d", step, issueID)
	}
	slog.Info("checkpoint: reverted", "issueID", issueID, "step", step, "repos", len(results))
	return results, nil
}

// LastPassingStep returns the highest step whose gate_status='pass' (a gate_run
// that passed), if any. Used by the gate auto-revert path to rewind to a
// known-good baseline before re-engaging the agent.
func (s *CheckpointService) LastPassingStep(ctx context.Context, issueID int64) (int, bool) {
	if s == nil || s.db == nil {
		return 0, false
	}
	var step sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT MAX(step) FROM issue_checkpoints WHERE issue_id = ? AND gate_status = ?`,
		issueID, CheckpointGateStatusPass).Scan(&step)
	if err != nil || !step.Valid {
		return 0, false
	}
	return int(step.Int64), true
}

// RevertToLastPassing rewinds the issue's worktree(s) to the most recent
// gate-passing checkpoint. Returns the step reverted to and whether one existed.
func (s *CheckpointService) RevertToLastPassing(ctx context.Context, issueID int64) (int, bool, error) {
	step, ok := s.LastPassingStep(ctx, issueID)
	if !ok {
		return 0, false, nil
	}
	_, err := s.Revert(ctx, issueID, step)
	return step, true, err
}

// CheckpointIssueID returns the issue a checkpoint row belongs to (for authz /
// validation on the diff endpoint).
func (s *CheckpointService) CheckpointIssueID(ctx context.Context, checkpointID int64) (int64, bool) {
	if s == nil || s.db == nil {
		return 0, false
	}
	var issueID int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT issue_id FROM issue_checkpoints WHERE id = ?`, checkpointID).Scan(&issueID); err != nil {
		return 0, false
	}
	return issueID, true
}

// --- internals ---

// repoTargets resolves one snapshot target per repo worktree of the workspace,
// preferring the repository-joined rows (for repo_name + id); falls back to the
// bare worktree rows, then to the workspace path itself so a single-dir / test
// workspace still gets one checkpoint.
func (s *CheckpointService) repoTargets(ctx context.Context, workspaceID int64) []checkpointRepoTarget {
	if withRepo, err := s.q.ListWorktreesWithRepository(ctx, workspaceID); err == nil && len(withRepo) > 0 {
		out := make([]checkpointRepoTarget, 0, len(withRepo))
		for _, w := range withRepo {
			out = append(out, checkpointRepoTarget{repositoryID: w.RepositoryID, repoName: w.RName, worktreePath: w.WorktreePath})
		}
		return out
	}
	if bare, err := s.q.ListWorktrees(ctx, workspaceID); err == nil && len(bare) > 0 {
		out := make([]checkpointRepoTarget, 0, len(bare))
		for _, w := range bare {
			out = append(out, checkpointRepoTarget{repositoryID: w.RepositoryID, worktreePath: w.WorktreePath})
		}
		return out
	}
	if ws, err := s.q.GetWorkspace(ctx, workspaceID); err == nil && strings.TrimSpace(ws.Path) != "" {
		return []checkpointRepoTarget{{worktreePath: ws.Path}}
	}
	return nil
}

// nextStep returns MAX(step)+1 for the issue (1 for the first checkpoint).
func (s *CheckpointService) nextStep(ctx context.Context, issueID int64) int {
	var maxStep sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT MAX(step) FROM issue_checkpoints WHERE issue_id = ?`, issueID).Scan(&maxStep); err != nil {
		slog.Warn("checkpoint: read max step", "issueID", issueID, "error", err)
		return 1
	}
	if !maxStep.Valid {
		return 1
	}
	return int(maxStep.Int64) + 1
}

// lastCommitForRepo returns the most recent checkpoint commit for this (issue,repo)
// so the next snapshot chains onto it (making per-step diffs true deltas rather than
// cumulative-since-HEAD); empty when the repo has no prior checkpoint (WriteCheckpoint
// then falls back to HEAD). repositoryID==0 is the workspace-path fallback, whose rows
// store repository_id as NULL, so it must be matched with IS NULL — not `= 0`.
func (s *CheckpointService) lastCommitForRepo(ctx context.Context, issueID, repositoryID int64) string {
	var (
		commit sql.NullString
		err    error
	)
	if repositoryID == 0 {
		err = s.db.QueryRowContext(ctx,
			`SELECT commit_hash FROM issue_checkpoints
			 WHERE issue_id = ? AND repository_id IS NULL ORDER BY step DESC, id DESC LIMIT 1`,
			issueID).Scan(&commit)
	} else {
		err = s.db.QueryRowContext(ctx,
			`SELECT commit_hash FROM issue_checkpoints
			 WHERE issue_id = ? AND repository_id = ? ORDER BY step DESC, id DESC LIMIT 1`,
			issueID, repositoryID).Scan(&commit)
	}
	if err != nil {
		return ""
	}
	return commit.String
}

// insertRow persists one repo's checkpoint metadata and returns its row id. It uses
// INSERT ... RETURNING id (supported by both modernc SQLite ≥3.35 and Postgres, and
// used elsewhere in the store) rather than LastInsertId, which the pgx driver does
// not implement — with LastInsertId the returned id was silently 0 on Postgres.
func (s *CheckpointService) insertRow(ctx context.Context, issueID, workspaceID int64, t checkpointRepoTarget,
	step int, kind, gateStatus, label, ref, commit, parent string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO issue_checkpoints
			(issue_id, workspace_id, repository_id, repo_name, worktree_path, step, kind,
			 gate_status, label, git_ref, commit_hash, parent_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		issueID, workspaceID, nullableID(t.repositoryID), t.repoName, t.worktreePath, step, kind,
		gateStatus, label, ref, commit, parent).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// nullableID maps 0 -> NULL so a workspace-path fallback (no repository row) does
// not violate the FK.
func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}
