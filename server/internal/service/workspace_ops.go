package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/git"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// ErrWorkspaceRunning is returned by MarkWorkspaceDone when the workspace
// is currently running and cannot be flipped without first stopping the agent.
var ErrWorkspaceRunning = errors.New("workspace is currently running")

// ErrWorkspaceNotCompleted is returned by UnmarkWorkspaceDone when the
// workspace is not in 'completed' state; undo is only meaningful right after
// a mark-done.
var ErrWorkspaceNotCompleted = errors.New("workspace is not completed; cannot unmark")

// ErrInvalidPreviousStatus is returned when the caller asks UnmarkWorkspaceDone
// to restore the workspace to a status outside the allow-list (e.g. 'running',
// 'completed', or junk). The allow-list is the set of statuses mark-done is
// reachable from in normal flows.
var ErrInvalidPreviousStatus = errors.New("invalid previous status")

// unmarkAllowedPreviousStatuses is the closed set of statuses that
// UnmarkWorkspaceDone will restore to. 'running' is excluded because mark-done
// refuses to flip from running, so no client snapshot should ever carry it.
// 'completed' is excluded because restoring to 'completed' is a no-op that
// would mask a malformed call.
var unmarkAllowedPreviousStatuses = map[string]struct{}{
	"created":      {},
	"needs_review": {},
	"attention":    {},
	"paused":       {},
}

type WorkspaceOpsService struct {
	q         *store.Queries
	ws        *WorkspaceService
	kanbanSvc *KanbanService
	// bus is optional (nil in tests). When set, MarkWorkspaceDone publishes a
	// workspace_completed event so the Epic execution engine can advance waves.
	bus *event.Bus

	// Column-native floor gate deps (stage 4; wired via SetFloorGateDeps, all
	// nil-safe). See floor_gate.go.
	db              *store.DB        // raw SQL for applicability + floor_retry_count
	gateExec        GateSpecExecutor // spec executor; nil => no floor gate runs
	floorKicker     FloorRetryKicker // autohost re-engage on auto failure; nil => escalate
	floorRetryLimit int              // <=0 => defaultFloorRetryLimit
	floorMu         sync.Mutex
	floorInFlight   map[int64]struct{}

	// execEvents is optional (nil in tests). When set, floor-gate outcomes are
	// recorded on the linked issue's execution timeline (spec §23.7).
	execEvents *ExecEventService

	// checkpoints is optional (nil in tests). When set, the floor gate takes a
	// gate-passing hidden-ref checkpoint on green and, on an auto (autohost) failure,
	// rewinds the worktree to the last gate-passing checkpoint before re-engaging the
	// agent — the autohost 安全网 "gate 失败自动回退续跑". Wired via SetCheckpointService.
	checkpoints *CheckpointService
}

// SetExecEventService wires the execution-timeline recorder (spec §23.7). Optional.
func (s *WorkspaceOpsService) SetExecEventService(e *ExecEventService) { s.execEvents = e }

// SetCheckpointService wires the hidden-ref checkpoint recorder used by the floor
// gate (checkpoint-on-pass + revert-to-last-passing on auto failure). Optional /
// nil-safe; wired once at boot (server.New). See checkpoint.go.
func (s *WorkspaceOpsService) SetCheckpointService(c *CheckpointService) { s.checkpoints = c }

// recordIssueExec appends a timeline event for the workspace's linked issue. Best-effort.
func (s *WorkspaceOpsService) recordIssueExec(ctx context.Context, ws store.Workspace, kind, summary string) {
	if s.execEvents == nil || !ws.IssueID.Valid {
		return
	}
	s.execEvents.Record(ctx, ExecEvent{IssueID: ws.IssueID.Int64, WorkspaceID: ws.ID, Kind: kind, Summary: summary})
}

func NewWorkspaceOpsService(q *store.Queries, ws *WorkspaceService, kanbanSvc *KanbanService, bus *event.Bus) *WorkspaceOpsService {
	return &WorkspaceOpsService{q: q, ws: ws, kanbanSvc: kanbanSvc, bus: bus}
}

// publishWorkspaceCompleted emits a workspace_completed event for the given
// workspace + linked issue. Nil-safe (bus may be nil in tests).
func (s *WorkspaceOpsService) publishWorkspaceCompleted(workspaceID, issueID int64, success bool) {
	if s.bus == nil {
		return
	}
	payload, err := json.Marshal(event.WorkspaceCompletedEvent{
		WorkspaceID: workspaceID,
		IssueID:     issueID,
		Success:     success,
	})
	if err != nil {
		slog.Error("marshal workspace_completed event", "error", err, "workspaceID", workspaceID)
		return
	}
	s.bus.Publish(event.OutputEvent{
		Type:        event.EventWorkspaceCompleted,
		Content:     string(payload),
		Role:        "system",
		Ts:          time.Now().UnixMilli(),
		WorkspaceId: workspaceID,
	})
}

func (s *WorkspaceOpsService) CommitWorktree(ctx context.Context, workspaceID int64, worktreeName, message string) error {
	path, err := s.resolveWorktreePath(ctx, workspaceID, worktreeName)
	if err != nil {
		return err
	}
	return git.CommitAll(path, message)
}

func (s *WorkspaceOpsService) MergeWorktree(ctx context.Context, workspaceID int64, worktreeName, targetBranch string) error {
	path, err := s.resolveWorktreePath(ctx, workspaceID, worktreeName)
	if err != nil {
		return err
	}
	sourceBranch, err := git.CurrentBranch(path)
	if err != nil {
		return fmt.Errorf("get current branch: %w", err)
	}
	return git.Merge(path, sourceBranch, targetBranch)
}

// SyncBranchIntoWorktree fast-forwards the named worktree's currently checked-out
// branch to sourceBranch when possible, the inverse direction of MergeWorktree.
// Used by the Epic engine to pull the epic feature branch's accumulated child work
// into the epic's own workspace. Fast-forward-only on purpose: it must never create
// a merge commit or leave a live worktree (one an orchestration/review agent may be
// editing) in a conflicted state — if the worktree branch has diverged it refuses
// cleanly and the caller skips, leaving the epic feature branch as the source of truth.
func (s *WorkspaceOpsService) SyncBranchIntoWorktree(ctx context.Context, workspaceID int64, worktreeName, sourceBranch string) error {
	path, err := s.resolveWorktreePath(ctx, workspaceID, worktreeName)
	if err != nil {
		return err
	}
	return git.MergeFastForwardOnly(path, sourceBranch)
}

func (s *WorkspaceOpsService) PushWorktree(ctx context.Context, workspaceID int64, worktreeName string) error {
	path, err := s.resolveWorktreePath(ctx, workspaceID, worktreeName)
	if err != nil {
		return err
	}
	return git.PushSetUpstream(path)
}

func (s *WorkspaceOpsService) GenerateCommitMessage(ctx context.Context, workspaceID int64, worktreeName string) (string, error) {
	path, err := s.resolveWorktreePath(ctx, workspaceID, worktreeName)
	if err != nil {
		return "", err
	}
	return git.GenerateCommitMessage(path)
}

type WorktreeCompletionStep struct {
	Name          string `json:"name"`
	CommitMessage string `json:"commit_message"`
	TargetBranch  string `json:"target_branch,omitempty"`
}

type WorktreeCompletionResult struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "success" | "skipped" | "error"
	Error  string `json:"error,omitempty"`
}

func (s *WorkspaceOpsService) CompleteWorkspace(ctx context.Context, workspaceID int64, mode string, steps []WorktreeCompletionStep) ([]WorktreeCompletionResult, error) {
	results := make([]WorktreeCompletionResult, 0, len(steps))

	for _, step := range steps {
		path, err := s.resolveWorktreePath(ctx, workspaceID, step.Name)
		if err != nil {
			results = append(results, WorktreeCompletionResult{Name: step.Name, Status: "error", Error: err.Error()})
			return results, fmt.Errorf("resolve worktree %s: %w", step.Name, err)
		}

		statuses, err := git.Status(path)
		if err != nil {
			results = append(results, WorktreeCompletionResult{Name: step.Name, Status: "error", Error: err.Error()})
			return results, fmt.Errorf("git status %s: %w", step.Name, err)
		}

		if len(statuses) > 0 && step.CommitMessage != "" {
			if err := git.CommitAll(path, step.CommitMessage); err != nil {
				results = append(results, WorktreeCompletionResult{Name: step.Name, Status: "error", Error: err.Error()})
				return results, fmt.Errorf("commit %s: %w", step.Name, err)
			}
		}

		if mode == "merge" && step.TargetBranch != "" {
			sourceBranch, err := git.CurrentBranch(path)
			if err != nil {
				results = append(results, WorktreeCompletionResult{Name: step.Name, Status: "error", Error: err.Error()})
				return results, fmt.Errorf("get branch %s: %w", step.Name, err)
			}
			if err := git.Merge(path, sourceBranch, step.TargetBranch); err != nil {
				results = append(results, WorktreeCompletionResult{Name: step.Name, Status: "error", Error: err.Error()})
				return results, fmt.Errorf("merge %s: %w", step.Name, err)
			}
		} else if mode == "push" {
			if err := git.PushSetUpstream(path); err != nil {
				results = append(results, WorktreeCompletionResult{Name: step.Name, Status: "error", Error: err.Error()})
				return results, fmt.Errorf("push %s: %w", step.Name, err)
			}
		}
		// If mode == "merge" and TargetBranch is empty, skip (no merge target)

		results = append(results, WorktreeCompletionResult{Name: step.Name, Status: "success"})
	}

	// Funnel the completion flip through finalizeCompletion so 'completed' has a
	// single write point (§22.5 invariant). The git commit/merge above is the
	// explicit user-driven completion; floor-gating this path is a follow-on (the
	// stage-4 floor gate lands on the RequestWorkspaceCompletion entry, see §22.8).
	ws, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return results, fmt.Errorf("workspace not found: %w", err)
	}
	if _, err := s.finalizeCompletion(ctx, ws); err != nil {
		return results, err
	}
	return results, nil
}

func (s *WorkspaceOpsService) resolveWorktreePath(ctx context.Context, workspaceID int64, worktreeName string) (string, error) {
	ws, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return "", fmt.Errorf("workspace not found: %w", err)
	}
	groups, err := s.ws.ListWorktreeGroups(ctx, workspaceID)
	if err != nil {
		return "", fmt.Errorf("list worktree groups: %w", err)
	}
	for _, g := range groups {
		if g.Name == worktreeName {
			return g.Path, nil
		}
	}
	return "", fmt.Errorf("worktree %q not found in workspace %d (path: %s)", worktreeName, workspaceID, ws.Path)
}

// MarkWorkspaceDoneResult is the outcome of a successful MarkWorkspaceDone
// call. Warnings carries machine-readable codes for partial-success
// situations (workspace flipped to 'completed' but a downstream side-effect
// failed) so the handler can surface them in the HTTP response and the SPA
// can render a non-blocking toast.warning instead of toast.success.
type MarkWorkspaceDoneResult struct {
	Warnings []string
}

// MarkWorkspaceDone flips workspace.status to 'completed' and the linked
// issue's lifecycle_status to 'completed'. Refuses if workspace is running.
// No git operations. Idempotent — calling it on an already-completed workspace
// is a no-op success.
//
// When workspace.status was flipped successfully but the issue lifecycle
// sync failed, the function still returns nil error (workspace transition is
// authoritative; the lifecycle drift is recoverable) but appends
// "issue_lifecycle_sync_failed" to result.Warnings so the caller can
// distinguish full success from partial success.
func (s *WorkspaceOpsService) MarkWorkspaceDone(ctx context.Context, workspaceID int64) (MarkWorkspaceDoneResult, error) {
	ws, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return MarkWorkspaceDoneResult{}, fmt.Errorf("workspace not found: %w", err)
	}
	if ws.Status == "running" {
		return MarkWorkspaceDoneResult{}, ErrWorkspaceRunning
	}
	if ws.Status == "completed" {
		return MarkWorkspaceDoneResult{}, nil // idempotent
	}
	return s.finalizeCompletion(ctx, ws)
}

// finalizeCompletion is the SINGLE write point for workspace.status='completed'
// (spec §22.5 invariant: "status='completed' 只能由 RequestWorkspaceCompletion ->
// finalize 写"). It flips the status, reverse-syncs the linked issue's lifecycle to
// completed (non-fatal — partial success is surfaced via Warnings), and publishes
// workspace_completed so the Epic execution engine advances waves.
//
// Callers own the guards (workspace not running, not already completed). Do NOT add
// another UpdateWorkspaceStatus('completed') anywhere in the codebase — route through
// here so the floor gate (RequestWorkspaceCompletion) can never be bypassed and the
// invariant test (TestCompletedWrittenOnlyByFinalize) stays meaningful.
func (s *WorkspaceOpsService) finalizeCompletion(ctx context.Context, ws store.Workspace) (MarkWorkspaceDoneResult, error) {
	// Autohost 安全网: 收尾 snapshot — capture the final worktree state as a
	// checkpoint before the workspace is marked completed, closing the timeline with
	// a restore point (covers the no-floor-gate finalize path too). Best-effort;
	// nil-safe; never blocks completion.
	if s.checkpoints != nil && ws.IssueID.Valid {
		if _, err := s.checkpoints.Snapshot(ctx, ws.IssueID.Int64, ws.ID,
			CheckpointKindAutohostFinal, "收尾", ""); err != nil {
			slog.Warn("finalize: autohost-final checkpoint failed", "workspaceID", ws.ID, "error", err)
		}
	}
	if err := s.q.UpdateWorkspaceStatus(ctx, store.UpdateWorkspaceStatusParams{
		Status: "completed",
		ID:     ws.ID,
	}); err != nil {
		return MarkWorkspaceDoneResult{}, fmt.Errorf("update workspace status: %w", err)
	}
	var warnings []string
	if ws.IssueID.Valid {
		if _, lcErr := s.kanbanSvc.UpdateIssueLifecycle(ctx, ws.IssueID.Int64, "completed"); lcErr != nil {
			slog.Error("update issue lifecycle on completion finalize",
				"error", lcErr,
				"workspaceID", ws.ID,
				"issueID", ws.IssueID.Int64,
			)
			// Do not fail: workspace status is already 'completed'; issue lifecycle
			// drift is recoverable via re-edit. Surface as a warning so the SPA
			// renders toast.warning instead of toast.success.
			warnings = append(warnings, "issue_lifecycle_sync_failed")
		}
		// Resolve a stale in-flight exec_status to the terminal 'done'. Completion is
		// the truthful terminal: the workspace finished and cleared the floor gate (or
		// had none). The floor-gate pass path (onFloorGateDone) already wrote 'done'
		// before calling here, so this is a no-op there. The no-floor-gate path lands
		// here with exec_status still active; OnWorkspaceCompleted only writes 'done'
		// back for epics and epic children, so a STANDALONE issue whose exec_status was
		// driven to an active state (e.g. ask_user answered -> 'running') would
		// otherwise stay stuck showing the in-flight badge forever. Only rewrite active
		// states so issues that never started execution ('idle') don't grow a spurious
		// "已完成" badge, and terminal/parked states (failed/gate_blocked/abandoned)
		// aren't stomped.
		if iss, gerr := s.q.GetIssue(ctx, ws.IssueID.Int64); gerr == nil && isActiveExecStatus(iss.ExecStatus) {
			if err := s.q.SetIssueExecStatus(ctx, store.SetIssueExecStatusParams{
				ExecStatus: "done", ID: ws.IssueID.Int64,
			}); err != nil {
				slog.Warn("finalize: reset stale exec_status to done",
					"workspaceID", ws.ID, "issueID", ws.IssueID.Int64, "error", err)
			}
		}
		// Notify the Epic execution engine (event-driven wave advance). success=true
		// because finalize means the issue cleared the floor gate (or had none).
		s.publishWorkspaceCompleted(ws.ID, ws.IssueID.Int64, true)
	}
	return MarkWorkspaceDoneResult{Warnings: warnings}, nil
}

// isActiveExecStatus reports whether an issue.exec_status represents an in-flight
// (non-terminal) execution state that a successful workspace completion should
// resolve to 'done'. The neutral 'idle' and the terminal/parked states (done,
// failed, gate_blocked, abandoned) are left untouched so completion never
// fabricates or rewrites a settled badge.
func isActiveExecStatus(status string) bool {
	switch status {
	case "running", "reviewing", "gate_checking", "waiting_input":
		return true
	}
	return false
}

// UnmarkWorkspaceDone reverts a workspace that was just flipped to 'completed'
// back to a caller-supplied previous status. The previous status comes from
// the SPA's optimistic-snapshot for the brief undo window after mark-done.
//
// Guards:
//   - Current status must be 'completed' — if a background process already
//     moved the workspace off completed, the undo is meaningless and we'd
//     otherwise stomp on the newer state.
//   - previousStatus must be in the allow-list — protects against a malformed
//     client passing 'running' (or anything else) and corrupting state.
//
// Spec §23.7: the linked issue's lifecycle IS reverted off 'completed' here, so the
// card stops claiming "完成" the instant the workspace is un-completed (§2 "never
// lie"). finalizeCompletion flipped lifecycle -> 'completed'; undo maps the restored
// workspace status back to an active lifecycle. A concurrent lifecycle edit racing
// this is acceptable — the next workspace transition re-syncs, and lying is worse.
func (s *WorkspaceOpsService) UnmarkWorkspaceDone(ctx context.Context, workspaceID int64, previousStatus string) error {
	if _, ok := unmarkAllowedPreviousStatuses[previousStatus]; !ok {
		return ErrInvalidPreviousStatus
	}
	ws, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("workspace not found: %w", err)
	}
	if ws.Status != "completed" {
		return ErrWorkspaceNotCompleted
	}
	if err := s.q.UpdateWorkspaceStatus(ctx, store.UpdateWorkspaceStatusParams{
		Status: previousStatus,
		ID:     workspaceID,
	}); err != nil {
		return fmt.Errorf("update workspace status: %w", err)
	}
	// Re-project the card off the Done lifecycle (spec §23.7). Non-fatal: a failed
	// revert leaves the workspace correctly un-completed, only the card lags.
	if ws.IssueID.Valid {
		target := lifecycleForRestoredStatus(previousStatus)
		if _, lcErr := s.kanbanSvc.UpdateIssueLifecycle(ctx, ws.IssueID.Int64, target); lcErr != nil {
			slog.Warn("unmark: revert issue lifecycle", "workspaceID", workspaceID, "issueID", ws.IssueID.Int64, "error", lcErr)
		}
		s.recordIssueExec(ctx, ws, "intervention", "撤销完成(undo)")
	}
	return nil
}

// lifecycleForRestoredStatus maps the workspace status the undo restores to onto an
// active (non-completed) issue lifecycle, so the card leaves the Done column. The
// exact prior lifecycle is not snapshotted; these map to the closest "in flight"
// stage (all are valid lifecycle_status values, see kanban.validLifecycleStatuses).
func lifecycleForRestoredStatus(previousStatus string) string {
	switch previousStatus {
	case "needs_review":
		return "implement-review"
	case "created":
		return "created"
	default: // attention / paused / anything active
		return "implement"
	}
}
