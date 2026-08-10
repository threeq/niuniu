package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// Sentinel errors for RunService — handlers map these to typed HTTP statuses
// and sanitized messages. Tests assert via errors.Is.
var (
	// ErrTargetColumnNotInProject: target column belongs to a different project.
	ErrTargetColumnNotInProject = errors.New("target column not in issue's project")
)

// RunService is the unique mutation entry for Issue.column_id via
// MoveIssueRunAware (the 看板+AI 列移动主干道, spec §4.2). Since the workflow/
// template-run decommission it is intentionally slim: it carries the single
// bare-board move branch (no active template Run) plus the column-native
// exit-gate / instruct-column / last-writer side effects. KanbanService.MoveIssue
// delegates here so every column move flows through one place.
type RunService struct {
	db                 *store.DB
	q                  *store.Queries
	bus                *event.Bus               // direct dependency on project's event hub; nil-safe
	instructDispatcher ColumnInstructDispatcher // optional; set via WithInstructDispatcher
	exitGateDispatcher ExitGateDispatcher       // optional; set via WithExitGateDispatcher
	activitySvc        *IssueActivityService    // optional; set via WithActivityService
}

// ColumnInstructDispatcher is called after a human drag lands an issue in a
// column with op_primitive='instruct'. It ensures the issue's workspace exists
// and sends the column's instruction to its agent (spec §3/§6 stage 3).
// Implemented by EpicExecutionService; nil-safe here.
type ColumnInstructDispatcher interface {
	DispatchInstructColumn(ctx context.Context, issueID, columnID, callerUserID int64)
}

// ExitGateDispatcher runs a source column's issue-bound exit gate (its
// applicability='if_routed' specs) after an issue leaves that column. Column-native
// and issue-bound (no harness_run); implemented by WorkspaceOpsService.RunExitGate.
// nil-safe here.
type ExitGateDispatcher interface {
	RunExitGate(ctx context.Context, issueID, srcColumnID int64)
}

func NewRunService(db *sql.DB, q *store.Queries, bus *event.Bus) *RunService {
	return &RunService{db: store.Wrap(db), q: q, bus: bus}
}

// WithInstructDispatcher wires the instruct-column side-effect handler used by
// BranchBareNoRun when a human drags an issue into an instruct-primitive column.
func (s *RunService) WithInstructDispatcher(d ColumnInstructDispatcher) {
	s.instructDispatcher = d
}

// WithExitGateDispatcher wires the column-native exit-gate handler invoked by
// BranchBareNoRun when an issue leaves a column that binds if_routed gate specs.
func (s *RunService) WithExitGateDispatcher(d ExitGateDispatcher) {
	s.exitGateDispatcher = d
}

// WithActivityService wires the issue-activity recorder so a cross-column move
// lands a "moved" entry on the issue timeline. Optional / nil-safe; every column
// move flows through MoveIssueRunAware, so this covers human drag and the AI
// advance_issue / abandon_issue paths alike.
func (s *RunService) WithActivityService(a *IssueActivityService) {
	s.activitySvc = a
}

// lockIssueRow acquires a row-level lock on the given issue within tx.
// On PostgreSQL it appends FOR UPDATE for a proper write-intent lock.
// On SQLite the tx already holds the DB write lock (BEGIN IMMEDIATE via
// _txlock=immediate), so the plain SELECT is a semantically equivalent no-op
// that stays syntactically valid.
func lockIssueRow(ctx context.Context, tx *store.Tx, issueID int64) error {
	var q string
	if store.Driver == "postgres" {
		// port() in the Tx wrapper converts ? → $1; FOR UPDATE is kept as-is.
		q = "SELECT id FROM issues WHERE id = ? FOR UPDATE"
	} else {
		q = "SELECT id FROM issues WHERE id = ?"
	}
	var id int64
	return tx.QueryRowContext(ctx, q, issueID).Scan(&id)
}

// ---------------------------------------------------------------------------
// MoveIssueRunAware — the keystone column-move main road (spec §4.2)
// ---------------------------------------------------------------------------

// MoveSource identifies who initiated a MoveIssue call (audit trail).
// All sources converge on the same path — source does not change the mechanics,
// only the event payload and a couple of side-effect guards (instruct dispatch
// fires only on drag; last-writer interrupt only on drag).
type MoveSource string

const (
	MoveSourceDrag        MoveSource = "drag"
	MoveSourceMCPAdvance  MoveSource = "mcp_phase_advance"
	MoveSourceMCPComplete MoveSource = "mcp_phase_complete"
	MoveSourceMCPRollback MoveSource = "mcp_phase_rollback"
)

// MoveBranch identifies which row of the §4.2 decision matrix was applied.
type MoveBranch string

const (
	BranchBareNoRun MoveBranch = "bare_no_run" // no active template Run
)

// MoveIssueInput carries all parameters for a MoveIssueRunAware call.
type MoveIssueInput struct {
	IssueID        int64
	TargetColumnID int64
	TargetPosition int64
	// Source identifies the initiator for audit events; does not change semantics.
	Source MoveSource
	// ActorUserID is the human / agent user attributing the move (audit
	// log). 0 means "not provided" (legacy callers).
	ActorUserID int64
}

// MoveOutcome describes what happened during MoveIssueRunAware.
type MoveOutcome struct {
	Branch    MoveBranch // which matrix row fired
	RunID     int64      // always 0 since template runs are decommissioned
	NewStatus string     // always "" (no Run state to report)
}

// MoveIssueRunAware is the unique mutation entry for Issue.column_id (spec §4.2).
// Both human drag (POST /api/issues/:id/move) and MCP advance converge here. The
// column_id write + lifecycle reverse-sync commit in one tx; async side effects
// (instruct dispatch, exit-gate, last-writer interrupt) fire post-commit.
//
// Returns the resulting MoveOutcome so handlers can render the right UI hint.
func (s *RunService) MoveIssueRunAware(ctx context.Context, in MoveIssueInput) (MoveOutcome, error) {
	out := MoveOutcome{}

	tx, err := s.db.BeginTx(ctx, nil) // SQLite IMMEDIATE via db.SetMaxOpenConns(1)
	if err != nil {
		return out, err
	}
	defer tx.Rollback() //nolint:errcheck

	// Acquire an issue row lock before any read (serialises concurrent moves of
	// the same issue — spec §4.5).
	if err := lockIssueRow(ctx, tx, in.IssueID); err != nil {
		return out, fmt.Errorf("lock issue row: %w", err)
	}

	q := tx.Queries()

	issue, err := q.GetIssue(ctx, in.IssueID)
	if err != nil {
		return out, fmt.Errorf("get issue: %w", err)
	}
	targetCol, err := q.GetColumn(ctx, in.TargetColumnID)
	if err != nil {
		return out, fmt.Errorf("get target column: %w", err)
	}
	srcCol, err := q.GetColumn(ctx, issue.ColumnID)
	if err != nil {
		return out, fmt.Errorf("get source column: %w", err)
	}
	if targetCol.ProjectID != srcCol.ProjectID {
		return out, ErrTargetColumnNotInProject
	}

	// Bare board move: plain column update + lifecycle reverse sync.
	if err := q.MoveIssue(ctx, store.MoveIssueParams{
		ColumnID: in.TargetColumnID, Position: in.TargetPosition, ID: in.IssueID,
	}); err != nil {
		return out, fmt.Errorf("move issue: %w", err)
	}
	// Reverse-sync lifecycle_status from target column's lifecycle_mapping (spec §4.2 Direction 2).
	if err := applyLifecycleReverseSync(ctx, q, nil, issue, in.TargetColumnID); err != nil {
		return out, fmt.Errorf("lifecycle reverse sync: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return out, err
	}
	out.Branch = BranchBareNoRun
	// Post-commit: record a "moved" activity on the issue timeline for a real
	// cross-column move (best-effort; nil-safe). srcCol/targetCol were loaded
	// in-tx above, so no extra lookup is needed. A same-column reorder
	// (target == source) is not a move and logs nothing.
	if in.TargetColumnID != srcCol.ID && s.activitySvc != nil {
		s.activitySvc.LogMoved(ctx, in.IssueID, srcCol.Name, targetCol.Name, "")
	}
	// NOTE: a human drag does NOT interrupt the in-flight agent (decision
	// 2026-06-08). Moving a card — to ANY column, instruct or not — never kills
	// the running session; the new column instruction (for instruct targets,
	// below) is QUEUED behind the in-flight turn and runs after it, consistent
	// with a manual chat send ("running -> Type to queue a message"). Explicit
	// abort stays a separate, deliberate action via the chat stop (X) button.
	// Earlier the §18 last-writer rule stopped the agent here, which made the
	// subsequent instruct dispatch see an idle session and send immediately
	// instead of queuing — the bug this removal fixes.
	//
	// AI-native stage 3 (spec §3/§6): a human drag into an instruct-primitive
	// column triggers workspace creation + column instruction delivery. The MCP
	// advance_issue path (AdvanceIssue) already handles this internally; skip
	// for MoveSourceMCPAdvance to avoid a double kickoff.
	if s.instructDispatcher != nil && in.Source == MoveSourceDrag {
		go s.instructDispatcher.DispatchInstructColumn(context.Background(), in.IssueID, in.TargetColumnID, in.ActorUserID)
	}
	// Column-native exit gate: leaving the source column runs its
	// applicability='if_routed' specs against the issue's workspace (async,
	// issue-bound; no-op when the source column binds no if_routed specs).
	// Detached context: the gate (build/test) outlives the request.
	if s.exitGateDispatcher != nil && in.TargetColumnID != srcCol.ID {
		go s.exitGateDispatcher.RunExitGate(context.WithoutCancel(ctx), in.IssueID, srcCol.ID)
	}
	s.publishKanbanUpdate(targetCol.ProjectID, in.IssueID)
	return out, nil
}

// ---------------------------------------------------------------------------
// Event helpers (post-commit, nil-safe)
// ---------------------------------------------------------------------------

// publishKanbanUpdate fires kanban_update event for SPA board refresh.
func (s *RunService) publishKanbanUpdate(projectID, issueID int64) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(event.OutputEvent{
		Type: event.EventKanbanUpdate, Role: "system", Ts: time.Now().UnixMilli(),
		Content: fmt.Sprintf(`{"projectId":%d,"issueId":%d,"action":"moved"}`, projectID, issueID),
	})
}
