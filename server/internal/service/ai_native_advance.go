package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/git"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// AI-native board execution, stage 3 (spec 2026-06-05-ai-native-board-execution
// -design.md §3/§6): the generic ensureWorkspace prelude, the `instruct` entry
// command, and the standalone `advance_issue` MCP tool that lets the orchestration
// agent self-report progress by moving a card between columns (skip / back allowed).
//
// op_primitive / when_to_use / phase_prompt(op_instruction) are read with raw
// `*store.DB` SQL rather than sqlc: stage 1a added op_primitive/when_to_use to the
// `columns` table via migrate.go ONLY (deliberately not in schema.sql, so the sqlc
// Column struct is not widened yet — see migrate.go migrateAINativeBoardStage1a).
// The full sqlc/DTO wiring is a later stage's job; stage 3 needs only to READ these
// fields, which a single anchored `WHERE id = ?` query does cross-dialect safely.

// GoalAssessment is the service-layer verdict for a freshly created issue: whether
// to auto-kickoff (Actionable), a reason for the timeline, and the inferred
// goal_condition. Mirrors agentproxy.IssueAssessment (mapped in the server adapter)
// to keep the service free of an agentproxy import.
type GoalAssessment struct {
	Actionable    bool
	Reason        string
	GoalCondition string
}

// GoalConditionSuggester infers a one-line completion criterion (goal_condition)
// for an issue from its title + description. Implemented in the server wiring over
// agentproxy.SuggestGoalCondition (haiku, the same chain the AI-suggest endpoint
// uses). Optional: nil in tests and when the claude CLI is unavailable, in which
// case ensureWorkspace falls back to the issue title+description as the goal.
type GoalConditionSuggester interface {
	// Suggest infers only a goal_condition (used by the drag-into-instruct path).
	Suggest(ctx context.Context, userID int64, title, description string) (string, error)
	// Classify returns the combined actionable verdict + goal_condition (used by the
	// create path's auto-kickoff gate, spec 2026-06-06).
	Classify(ctx context.Context, userID int64, title, description string) (*GoalAssessment, error)
}

// SetGoalSuggester wires the goal_condition suggester used by ensureWorkspace to
// infer a goal for a brand-new standalone workspace. Optional; nil-safe.
func (s *EpicExecutionService) SetGoalSuggester(g GoalConditionSuggester) {
	s.goalSuggester = g
}

// SetAsyncRunner overrides how the engine spawns detached side-tasks (the create
// path's classify+kickoff). Tests set an inline runner; production leaves it nil.
func (s *EpicExecutionService) SetAsyncRunner(r func(func())) { s.runAsync = r }

func (s *EpicExecutionService) goAsync(f func()) {
	if s.runAsync != nil {
		s.runAsync(f)
		return
	}
	go f()
}

// columnOp holds a column's AI-native command fields (read via raw SQL, see file
// header). op_instruction reuses the existing phase_prompt column (spec §11.1).
type columnOp struct {
	id          int64
	name        string
	opPrimitive string // none | instruct | complete
	instruction string // phase_prompt, may be empty
	lifecycle   string // lifecycle_mapping, e.g. "implement" / "implement-review"
}

// getColumnOp reads a column's op_primitive + name + op_instruction + lifecycle
// by id. The `id = ?` predicate anchors the parameter to a typed column, so the
// query is safe on both SQLite and PostgreSQL (no 42P18 parse-time inference gap).
func (s *EpicExecutionService) getColumnOp(ctx context.Context, columnID int64) (columnOp, error) {
	var (
		name        string
		opPrimitive string
		phasePrompt sql.NullString
		lifecycle   sql.NullString
	)
	row := s.db.QueryRowContext(ctx,
		`SELECT name, op_primitive, phase_prompt, lifecycle_mapping FROM columns WHERE id = ?`, columnID)
	if err := row.Scan(&name, &opPrimitive, &phasePrompt, &lifecycle); err != nil {
		return columnOp{}, fmt.Errorf("read column op fields: %w", err)
	}
	return columnOp{
		id:          columnID,
		name:        name,
		opPrimitive: opPrimitive,
		instruction: strings.TrimSpace(phasePrompt.String),
		lifecycle:   strings.TrimSpace(lifecycle.String),
	}, nil
}

// AdvanceIssueInput is the request for AdvanceIssue (the advance_issue MCP tool).
type AdvanceIssueInput struct {
	IssueID int64
	// ToColumn is the destination column, given either as a numeric id or as a
	// column name (case-insensitive) within the issue's project. Names are the
	// natural reference for an agent reading the looped-in column menu.
	ToColumn string
	// Reason is the agent's free-text rationale for the move (logged; audited in a
	// later stage). Optional.
	Reason string
	// CallerUserID is the authenticated MCP/API user, threaded into the move (for
	// the kanban event) and into goal_condition inference (account resolution).
	CallerUserID int64
	// InjectReviewContext forces the two-layer review context (#623) to be woven
	// into the instruct kickoff even when the source column is not a review lane —
	// set by RequestChanges so a bounce works from ANY column, including 完成
	// (re-open a done issue for re-review). When false, injection still fires on a
	// natural review→implement transition (a human drag / review-agent advance).
	InjectReviewContext bool
}

// AdvanceIssueResult is the response for AdvanceIssue.
type AdvanceIssueResult struct {
	IssueID     int64  `json:"issue_id"`
	ColumnID    int64  `json:"column_id"`
	ColumnName  string `json:"column_name"`
	OpPrimitive string `json:"op_primitive"`
	WorkspaceID int64  `json:"workspace_id,omitempty"`
	Reused      bool   `json:"workspace_reused,omitempty"`
	Instructed  bool   `json:"instructed"`
	// RouteSteps is the issue's cumulative advance_issue step count after this move
	// (spec §23.2). Blocked reports that a routing cap (re-entry / total steps) tripped:
	// the issue landed blocked-needs-human and no instruction was sent.
	RouteSteps    int    `json:"route_steps,omitempty"`
	Blocked       bool   `json:"blocked,omitempty"`
	BlockedReason string `json:"blocked_reason,omitempty"`
}

// AdvanceIssue moves an issue to another column and, when the destination is an
// `instruct` column, ensures the issue's workspace exists and sends the column's
// instruction to its agent (spec §6). Skips and back-tracks are allowed — the
// destination need not be adjacent or "forward". The move itself reuses the
// run-aware kanban move (a plain column update + lifecycle reverse-sync for a
// runless, new-model issue), which validates the target is in the same project,
// so a cross-project / cross-tenant column is rejected before any side effect.
//
// A `none` destination is a plain move. A `complete` destination first passes the
// uncommitted-code gate (see completionDirty): while the issue's workspace still
// has uncommitted changes the move is REFUSED and the card stays put — the agent
// must commit before routing to done. For an EPIC CHILD the integration itself
// then runs inline: its worktree(s) are committed + merged into the epic feature
// branch before the card moves (a conflict refuses the move). The column-native
// floor gate itself still lives at the completion choke point (RequestWorkspace
// Completion, stage 4); wiring a `complete`-column move to invoke it — with the
// §22.7 per-issue-type trigger — is a follow-on.
func (s *EpicExecutionService) AdvanceIssue(ctx context.Context, in AdvanceIssueInput) (AdvanceIssueResult, error) {
	issue, err := s.q.GetIssue(ctx, in.IssueID)
	if err != nil {
		return AdvanceIssueResult{}, fmt.Errorf("load issue %d: %w", in.IssueID, err)
	}
	srcCol, err := s.q.GetColumn(ctx, issue.ColumnID)
	if err != nil {
		return AdvanceIssueResult{}, fmt.Errorf("load source column: %w", err)
	}

	targetID, err := s.resolveColumnID(ctx, srcCol.ProjectID, in.ToColumn)
	if err != nil {
		return AdvanceIssueResult{}, err
	}
	target, err := s.getColumnOp(ctx, targetID)
	if err != nil {
		return AdvanceIssueResult{}, err
	}

	// Uncommitted-code gate for the done lane: checked BEFORE the move so a dirty
	// workspace leaves the card exactly where it is. Only the AI advance path is
	// gated — a human drag (RunService/kanban) is not routed through here.
	if target.opPrimitive == "complete" {
		if dirty, detail := s.completionDirty(ctx, in.IssueID); dirty {
			reason := fmt.Sprintf(
				"工作空间存在未提交的代码改动（%s），不能移动到完成列「%s」。请先提交全部改动并按工作流合并到目标分支，再重试 advance_issue。",
				detail, target.name)
			slog.Info("advance_issue: refused move to complete column (uncommitted changes)",
				"issueID", in.IssueID, "to", targetID, "detail", detail)
			if s.execEvents != nil {
				s.execEvents.Record(ctx, ExecEvent{
					IssueID: in.IssueID, Kind: "intervention",
					Summary: "拒绝移动到「" + target.name + "」：存在未提交代码",
				})
			}
			return AdvanceIssueResult{}, errors.New(reason)
		}
		// Epic child entering the done lane: integrate its worktree(s) into the epic
		// feature branch NOW, before the move. advance_issue is the canonical "last
		// step" of a child — relying on a later agent_done to merge (the previous
		// behaviour) routinely left the card in 完成 with its code never landing on
		// epic/<id>, stalling the epic. A merge failure refuses the move so the card
		// stays put and the child agent gets an actionable error to resolve.
		if issue.ParentIssueID.Valid {
			if ws, ok := s.activeWorkspaceForIssue(ctx, in.IssueID); ok {
				if !s.mergeChildIntoEpic(ctx, ws.ID, issue) {
					branch := epicBranchName(issue.ParentIssueID.Int64)
					reason := fmt.Sprintf(
						"代码未能合入 epic 分支 %s（多为合并冲突），已拒绝移动到完成列「%s」。请在工作空间内合并 %s 解决冲突并提交后，重试 advance_issue。",
						branch, target.name, branch)
					slog.Info("advance_issue: refused move to complete column (epic merge failed)",
						"issueID", in.IssueID, "to", targetID, "branch", branch)
					if s.execEvents != nil {
						s.execEvents.Record(ctx, ExecEvent{
							IssueID: in.IssueID, WorkspaceID: ws.ID, Kind: "intervention",
							Summary: "拒绝移动到「" + target.name + "」：合入 epic 分支失败",
						})
					}
					return AdvanceIssueResult{}, errors.New(reason)
				}
				if s.execEvents != nil {
					s.execEvents.Record(ctx, ExecEvent{
						IssueID: in.IssueID, WorkspaceID: ws.ID, Kind: "advance",
						Summary: "代码已合入 epic 分支 " + epicBranchName(issue.ParentIssueID.Int64),
					})
				}
			}
		}
	}

	// Move the card. MoveIssue is idempotent for a same-column move and validates
	// the target is in the issue's project. callerUserID drives the kanban event.
	// The MCP source marks this as the AI's own move so the §18 last-writer rule
	// does not interrupt the agent that is performing this very advance.
	if err := s.kanban.MoveIssueWithSource(ctx, in.IssueID, targetID, 0, in.CallerUserID, MoveSourceMCPAdvance); err != nil {
		return AdvanceIssueResult{}, fmt.Errorf("move issue: %w", err)
	}
	slog.Info("advance_issue: moved card",
		"issueID", in.IssueID, "from", issue.ColumnID, "to", targetID,
		"primitive", target.opPrimitive, "reason", in.Reason)
	if s.execEvents != nil {
		s.execEvents.Record(ctx, ExecEvent{
			IssueID: in.IssueID, Kind: "advance",
			Summary: fmt.Sprintf("移动到「%s」%s", target.name, reasonSuffix(in.Reason)),
		})
	}
	// Audit the routing decision (spec §18): org-owned boards get an org_audit_log
	// row for route-to-complete / skip / back; personal boards no-op.
	s.auditIssueDecision(ctx, issue, in.CallerUserID, "issue.advanced",
		resourceAuditPayload(issue.Title, "to_column", target.name, "primitive", target.opPrimitive, "reason", in.Reason))

	// Review-loop (#623): moving INTO a review column with a non-empty reason means
	// the agent finished a rework round — persist that reason as a durable issue
	// comment so the reviewer sees "本轮改动说明" in the issue thread, closing the
	// loop symmetrically with the review-time bounce comment.
	if isReviewColumnByFields(target.name, target.lifecycle) {
		s.recordReviewChangeSummary(ctx, in.IssueID, in.Reason)
	}

	res := AdvanceIssueResult{
		IssueID:     in.IssueID,
		ColumnID:    targetID,
		ColumnName:  target.name,
		OpPrimitive: target.opPrimitive,
	}

	// Routing livelock cap (spec §23.2): record this move and stop if the issue has
	// re-entered this column too many times or taken too many total routing steps.
	// This is a third terminator alongside the列级 gate retry and floor_retry_count
	// (§22.4) — "三者取最先触发". Over the cap, the issue lands blocked-needs-human
	// (exec_status='gate_blocked' + workspace -> attention) and NO instruction is sent
	// (re-engaging would just feed the ping-pong).
	reentry, total := s.recordRouteVisit(ctx, in.IssueID, targetID)
	res.RouteSteps = total
	if reentry > s.effectiveColumnReentryLimit() || total > s.effectiveRouteStepLimit() {
		reason := fmt.Sprintf("routing livelock: column re-entry %d/%d, total steps %d/%d",
			reentry, s.effectiveColumnReentryLimit(), total, s.effectiveRouteStepLimit())
		s.escalateRoutingBlocked(ctx, issue, reason)
		res.Blocked = true
		res.BlockedReason = reason
		return res, nil
	}

	if target.opPrimitive != "instruct" {
		return res, nil
	}

	// `instruct` destination: ensure the workspace, then send the column command.
	ws, reused, err := s.EnsureWorkspaceForIssue(ctx, in.IssueID, in.CallerUserID, true)
	if err != nil {
		// The move already landed; surface the workspace failure but keep the
		// (truthful) moved state in the result so the caller isn't misled.
		return res, fmt.Errorf("ensure workspace: %w", err)
	}
	res.WorkspaceID = ws.ID
	res.Reused = reused

	// Autohost 安全网: snapshot the worktree baseline as the issue enters the
	// implement column, so the checkpoint timeline has a starting point and any
	// later gate/autohost checkpoint has a diff base. Best-effort; nil-safe; a
	// snapshot failure never blocks the move or the kickoff.
	if s.checkpoints != nil {
		if _, err := s.checkpoints.Snapshot(ctx, in.IssueID, ws.ID,
			CheckpointKindAdvance, "进入「"+target.name+"」基线", ""); err != nil {
			slog.Warn("advance_issue: checkpoint snapshot failed", "issueID", in.IssueID, "error", err)
		}
	}

	// Review-loop context injection (#623): when the card is bounced from a review
	// column back into an implement (instruct) lane, weave the two-layer review
	// context — kanban issue comments (macro) + unresolved workspace diff comments
	// (micro) — into the kickoff. The message rides the same Deliver path a normal
	// turn uses, so the running autohost agent CONTINUES in its existing worktree
	// (changes intact) rather than restarting from scratch.
	var extra string
	var consumedDiffIDs []int64
	// Inject on a natural review→implement bounce, OR whenever the caller forces it
	// (RequestChanges) so re-opening a completed issue also carries the review
	// context. Never inject when landing back in a review lane.
	wantInject := (in.InjectReviewContext || isReviewColumnByFields(srcCol.Name, srcCol.LifecycleMapping)) &&
		!isReviewColumnByFields(target.name, target.lifecycle)
	if wantInject {
		extra, consumedDiffIDs = s.buildReviewReworkContext(ctx, issue)
	}

	// Send the column instruction as a command to the agent. For a brand-new
	// epic/child workspace the post-create hook (OnWorkspaceCreated) already kicked
	// the agent off with its own prompt, so we skip to avoid a double kickoff; the
	// instruction is sent only when reusing a running agent or when the workspace
	// was freshly created for a standalone issue (whose hook is a no-op).
	if reused || isStandaloneIssue(issue) {
		s.sendKickoff(ctx, ws, composeInstructMessage(target, issue, extra))
		res.Instructed = true
		// Mark the injected diff comments consumed so the next bounce does not
		// re-inject them (mirrors SendCommentToAgent's MarkCommentSent).
		for _, id := range consumedDiffIDs {
			if err := s.q.MarkCommentSent(ctx, id); err != nil {
				slog.Warn("advance_issue: mark diff comment consumed", "commentID", id, "error", err)
			}
		}
	}
	return res, nil
}

// OnIssueCreated is the create-into-column orchestration hook (spec §13 stage 8,
// §3 "创建 = 进入列"): when an issue is created DIRECTLY into an `instruct` column via
// the creator path, auto-start orchestration — ensure the issue's workspace and send
// the column's instruction to its agent, exactly as a drag into that column would
// (AdvanceIssue's instruct branch). A `none`/`complete` destination is a no-op.
// goal_condition is auto-inferred for a brand-new standalone workspace inside
// EnsureWorkspaceForIssue -> startStandaloneAutohost.
//
// This is the creator path only. The programmatic batch path (BatchCreateIssues)
// lands every task in the project's first column and never calls this hook, so it
// never auto-starts work (spec §3.1: "batch 路径不自动开工").
//
// Unlike AdvanceIssue this records NO routing step (a creation is not an
// advance_issue move) and does no card move (the issue is already in the column).
// The orchestration side may error after the issue itself was created; callers treat
// the hook as best-effort and do not fail the create response on an orchestration
// hiccup (the user can drag the card to retry).
func (s *EpicExecutionService) OnIssueCreated(ctx context.Context, issueID, callerUserID int64) (AdvanceIssueResult, error) {
	issue, err := s.q.GetIssue(ctx, issueID)
	if err != nil {
		return AdvanceIssueResult{}, fmt.Errorf("load issue %d: %w", issueID, err)
	}
	col, err := s.getColumnOp(ctx, issue.ColumnID)
	if err != nil {
		return AdvanceIssueResult{}, err
	}
	res := AdvanceIssueResult{
		IssueID:     issueID,
		ColumnID:    issue.ColumnID,
		ColumnName:  col.name,
		OpPrimitive: col.opPrimitive,
	}
	if col.opPrimitive != "instruct" {
		return res, nil
	}

	ws, reused, err := s.EnsureWorkspaceForIssue(ctx, issueID, callerUserID, false) // shell only
	if err != nil {
		return res, fmt.Errorf("ensure workspace: %w", err)
	}
	res.WorkspaceID = ws.ID
	res.Reused = reused

	// Auto-kickoff is gated by an async "is this an actionable task?" haiku verdict
	// (spec 2026-06-06 §4): build the shell now, decide whether to start the agent
	// out of band so the create response is never blocked. Conservative on any
	// error / missing suggester: build the shell, do not auto-start.
	s.goAsync(func() {
		s.classifyAndKickoff(context.Background(), ws, issue, col, reused, callerUserID)
	})
	slog.Info("onIssueCreated: shell built, kickoff gating async",
		"issueID", issueID, "columnID", issue.ColumnID, "workspaceID", ws.ID, "reused", reused)
	return res, nil
}

// classifyAndKickoff runs the create-path auto-kickoff gate: a haiku verdict on
// whether the issue is an actionable task. It always persists any inferred
// goal_condition (so a later manual start is pre-filled), then either kicks off
// the agent (actionable) or records a withheld-kickoff intervention event.
func (s *EpicExecutionService) classifyAndKickoff(ctx context.Context, ws store.Workspace, issue store.Issue, col columnOp, reused bool, callerUserID int64) {
	if s.goalSuggester == nil {
		s.recordKickoffSkipped(ctx, issue.ID, ws.ID, "判定不可用：未接入推断服务")
		return
	}
	assess, err := s.goalSuggester.Classify(ctx, callerUserID, issue.Title, nullStringVal(issue.Description))
	if err != nil || assess == nil {
		reason := "判定不可用"
		if err != nil {
			reason = "判定不可用：" + err.Error()
		}
		s.recordKickoffSkipped(ctx, issue.ID, ws.ID, reason)
		return
	}
	goal := strings.TrimSpace(assess.GoalCondition)
	if goal != "" {
		g := goal
		if len(g) > 4000 {
			g = g[:4000]
		}
		if err := s.q.UpdateIssueGoalCondition(ctx, store.UpdateIssueGoalConditionParams{GoalCondition: g, ID: issue.ID}); err != nil {
			slog.Warn("classifyAndKickoff: persist goal_condition", "issueID", issue.ID, "error", err)
		}
	}
	if !assess.Actionable {
		s.recordKickoffSkipped(ctx, issue.ID, ws.ID, "非明确任务："+assess.Reason)
		return
	}
	switch {
	case !reused && isStandaloneIssue(issue):
		s.kickoffStandaloneIssue(ctx, ws, issue, col, goal)
	case reused:
		s.sendKickoff(ctx, ws, composeInstructMessage(col, issue, ""))
	}
	// fresh epic/child: already kicked off by the post-create hook; nothing to do.
}

// kickoffStandaloneIssue puts a fresh standalone workspace into autohost (seeding
// goal) and sends the column instruction as the kickoff. This is the "start the
// agent" half that the create path defers until the actionable verdict.
// When the haiku classify call returned an empty GoalCondition, fall back to
// the issue title+description so the autohost judge always has a target.
func (s *EpicExecutionService) kickoffStandaloneIssue(ctx context.Context, ws store.Workspace, issue store.Issue, col columnOp, goal string) {
	if goal == "" {
		goal = composeIssueMarkdown(issue.Title, nullStringVal(issue.Description))
	}
	s.enableAutohost(ctx, ws.ID, issue, goal)
	s.sendKickoff(ctx, ws, composeInstructMessage(col, issue, ""))
}

// recordKickoffSkipped logs + records a withheld-auto-kickoff event on the issue's
// execution timeline so the card shows why it did not auto-start.
func (s *EpicExecutionService) recordKickoffSkipped(ctx context.Context, issueID, workspaceID int64, reason string) {
	slog.Info("onIssueCreated: auto-kickoff withheld", "issueID", issueID, "reason", reason)
	if s.execEvents != nil {
		s.execEvents.Record(ctx, ExecEvent{IssueID: issueID, WorkspaceID: workspaceID, Kind: "intervention", Summary: "未自动开工：" + reason})
	}
}

// DispatchInstructColumn implements RunService.ColumnInstructDispatcher for the
// drag-into-instruct path (spec §3/§6). Called asynchronously after the card
// move commits. Checks the column's op_primitive; is a no-op for non-instruct
// columns so RunService can call it unconditionally for all drag moves.
func (s *EpicExecutionService) DispatchInstructColumn(ctx context.Context, issueID, columnID, callerUserID int64) {
	col, err := s.getColumnOp(ctx, columnID)
	if err != nil {
		slog.Warn("drag-to-instruct: get column op", "columnID", columnID, "err", err)
		return
	}
	if col.opPrimitive != "instruct" {
		return
	}
	issue, err := s.q.GetIssue(ctx, issueID)
	if err != nil {
		slog.Warn("drag-to-instruct: load issue", "issueID", issueID, "err", err)
		return
	}
	ws, reused, err := s.EnsureWorkspaceForIssue(ctx, issueID, callerUserID, true)
	if err != nil {
		slog.Warn("drag-to-instruct: ensure workspace", "issueID", issueID, "err", err)
		return
	}
	if reused || isStandaloneIssue(issue) {
		s.sendKickoff(ctx, ws, composeInstructMessage(col, issue, ""))
	}
}

// EnsureWorkspaceForIssue is the generic prelude shared by the create-into-instruct
// and drag-into-instruct paths (spec §3): reuse the issue's existing non-archived
// workspace if one is running, otherwise create one (which kicks off the
// orchestration/child agent via the post-create hook) and, for a standalone issue,
// optionally put it into autohost with an inferred goal_condition.
// autoStartStandalone controls whether a freshly-created standalone workspace is
// immediately placed in autohost + goal seeded (drag path = true; create path = false,
// because the create path runs the actionable classify gate async first).
// Returns the workspace and whether it was reused.
func (s *EpicExecutionService) EnsureWorkspaceForIssue(ctx context.Context, issueID, callerUserID int64, autoStartStandalone bool) (store.Workspace, bool, error) {
	if existing, ok := s.activeWorkspaceForIssue(ctx, issueID); ok {
		slog.Info("ensureWorkspace: reused existing workspace", "issueID", issueID, "workspaceID", existing.ID)
		if issue, err := s.q.GetIssue(ctx, issueID); err == nil {
			s.injectBoardMenu(ctx, existing, issue) // refresh the board menu (idempotent)
		}
		return existing, true, nil
	}
	res, err := s.createWorkspaceForIssue(ctx, issueID, callerUserID)
	if err != nil {
		return store.Workspace{}, false, err
	}
	ws := res.Workspace
	slog.Info("ensureWorkspace: created workspace", "issueID", issueID, "workspaceID", ws.ID)

	// Epic / child issues are auto-linked by the post-create hook (OnWorkspace
	// Created): it sets autohost + goal + kickoff. A standalone issue's hook is a
	// no-op (manual control), so for the AI-native instruct path we enable autohost
	// + seed/infer its goal here — the column instruction that the caller sends next
	// is the kickoff.
	issue, err := s.q.GetIssue(ctx, issueID)
	if err != nil {
		slog.Warn("ensureWorkspace: reload issue", "issueID", issueID, "error", err)
		return ws, false, nil
	}
	// Inject the board column menu into the new workspace's agent instruction file
	// before any kickoff (spec §6). Best-effort; the caller's instruct command is the
	// kickoff. For epic/child workspaces the post-create hook injects + kicks off.
	s.injectBoardMenu(ctx, ws, issue)
	if isStandaloneIssue(issue) && autoStartStandalone {
		s.startStandaloneAutohost(ctx, ws, issue, callerUserID)
	}
	return ws, false, nil
}

// StartStandaloneIssueWithPrompt ensures a (standalone) issue has a running
// workspace in autohost mode and delivers prompt as the kickoff message. Unlike
// the drag/create paths it needs no executor column — it is the programmatic way
// to spin up and run an agent for a freshly created issue (e.g. the memory
// maintenance task). Returns the workspace.
func (s *EpicExecutionService) StartStandaloneIssueWithPrompt(ctx context.Context, issueID, callerUserID int64, prompt string) (store.Workspace, error) {
	ws, _, err := s.EnsureWorkspaceForIssue(ctx, issueID, callerUserID, true)
	if err != nil {
		return store.Workspace{}, err
	}
	s.sendKickoff(ctx, ws, prompt)
	return ws, nil
}

// startStandaloneAutohost puts a freshly-created standalone workspace into autohost
// mode and seeds its goal_condition. enableAutohost provides an immediate fallback
// goal (issue title+description) so the judge always has a target; when the issue
// had no user-set goal and a suggester is wired, a best-effort haiku inference
// refines it asynchronously (the judge first runs only after the agent idles, well
// after a ~tens-of-seconds inference, so the refined goal almost always wins; a
// lost race just leaves the perfectly-usable fallback in place).
func (s *EpicExecutionService) startStandaloneAutohost(ctx context.Context, ws store.Workspace, issue store.Issue, callerUserID int64) {
	hadGoal := strings.TrimSpace(issue.GoalCondition) != ""
	s.enableAutohost(ctx, ws.ID, issue, composeIssueMarkdown(issue.Title, nullStringVal(issue.Description)))
	if hadGoal || s.goalSuggester == nil {
		return
	}
	title := issue.Title
	desc := nullStringVal(issue.Description)
	issueID := issue.ID
	// Detached context: the MCP request ctx is cancelled when the tool returns, but
	// the inference (subprocess) outlives it. Best-effort: failures are logged only.
	go func() {
		suggestion, err := s.goalSuggester.Suggest(context.Background(), callerUserID, title, desc)
		if err != nil {
			slog.Warn("ensureWorkspace: goal_condition inference failed (keeping fallback)",
				"issueID", issueID, "error", err)
			return
		}
		suggestion = strings.TrimSpace(suggestion)
		if suggestion == "" {
			return
		}
		if len(suggestion) > 4000 {
			suggestion = suggestion[:4000]
		}
		if err := s.q.UpdateIssueGoalCondition(context.Background(),
			store.UpdateIssueGoalConditionParams{GoalCondition: suggestion, ID: issueID}); err != nil {
			slog.Warn("ensureWorkspace: persist inferred goal_condition", "issueID", issueID, "error", err)
			return
		}
		slog.Info("ensureWorkspace: inferred goal_condition", "issueID", issueID, "len", len(suggestion))
		// Audit the AI goal_condition inference (spec §18 key decision). actorUserID=0
		// marks it system/AI-driven. Reload the issue for its current column owner.
		if iss, gerr := s.q.GetIssue(context.Background(), issueID); gerr == nil {
			s.auditIssueDecision(context.Background(), iss, 0, "issue.goal_condition_inferred",
				resourceAuditPayload(title, "len", strconv.Itoa(len(suggestion))))
		}
	}()
}

// resolveColumnID resolves a column reference (numeric id OR case-insensitive name)
// to a column id within the given project. Both forms are validated against the
// project's columns, so an id from another project or an unknown name is rejected
// (a name that matches more than one column is ambiguous and also rejected).
func (s *EpicExecutionService) resolveColumnID(ctx context.Context, projectID int64, ref string) (int64, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return 0, errors.New("to_column is required")
	}
	cols, err := s.q.ListColumnsByProject(ctx, projectID)
	if err != nil {
		return 0, fmt.Errorf("list project columns: %w", err)
	}
	if id, perr := strconv.ParseInt(ref, 10, 64); perr == nil {
		for _, c := range cols {
			if c.ID == id {
				return id, nil
			}
		}
		return 0, fmt.Errorf("column id %d is not in this issue's project", id)
	}
	var match int64
	var n int
	for _, c := range cols {
		if strings.EqualFold(strings.TrimSpace(c.Name), ref) {
			match = c.ID
			n++
		}
	}
	switch n {
	case 0:
		return 0, fmt.Errorf("no column named %q in this issue's project", ref)
	case 1:
		return match, nil
	default:
		return 0, fmt.Errorf("column name %q is ambiguous (%d columns share it); use the column id", ref, n)
	}
}

// SetCompletionDirtyChecker overrides the uncommitted-code probe used by the
// AdvanceIssue complete-column gate. Tests inject a fake; production leaves it
// nil (= the git-status probe). Nil-safe.
func (s *EpicExecutionService) SetCompletionDirtyChecker(f func(ctx context.Context, ws store.Workspace) (bool, string)) {
	s.dirtyChecker = f
}

// completionDirty reports whether the issue's active workspace still has
// uncommitted code, blocking a move into a `complete` column. An issue with no
// active workspace has nothing to commit (e.g. a hand-managed card) and passes.
func (s *EpicExecutionService) completionDirty(ctx context.Context, issueID int64) (bool, string) {
	ws, ok := s.activeWorkspaceForIssue(ctx, issueID)
	if !ok {
		return false, ""
	}
	if s.dirtyChecker != nil {
		return s.dirtyChecker(ctx, ws)
	}
	return s.gitWorkspaceDirty(ctx, ws)
}

// gitWorkspaceDirty probes every repo worktree of a workspace with
// `git status --porcelain` (uncommitted tracked changes AND untracked files both
// count — "有代码没有提交"). Fail-OPEN: a path that cannot be probed (not a git
// repo, git binary missing) never blocks the move, so non-git workspaces and the
// fake workspaces in tests keep their previous behaviour; the gate fires only on
// definite dirt.
func (s *EpicExecutionService) gitWorkspaceDirty(ctx context.Context, ws store.Workspace) (bool, string) {
	var paths []string
	if wts, err := s.q.ListWorktrees(ctx, ws.ID); err == nil && len(wts) > 0 {
		for _, wt := range wts {
			paths = append(paths, wt.WorktreePath)
		}
	} else if ws.Path != "" {
		paths = []string{ws.Path}
	}
	for _, p := range paths {
		entries, err := git.Status(p)
		if err != nil {
			continue
		}
		if len(entries) > 0 {
			return true, fmt.Sprintf("%s 有 %d 个未提交变更", filepath.Base(p), len(entries))
		}
	}
	return false, ""
}

const (
	// defaultRouteStepLimit bounds the total advance_issue moves for one issue.
	defaultRouteStepLimit = 20
	// defaultColumnReentryLimit bounds how many times an issue may enter any single
	// column (an implement<->review ping-pong trips this well before the step cap).
	defaultColumnReentryLimit = 4
)

// SetRoutingLimits configures the advance_issue livelock caps (spec §23.2). Either
// value <=0 keeps the package default. Wired once at boot (server.New, from env).
func (s *EpicExecutionService) SetRoutingLimits(stepLimit, reentryLimit int) {
	s.routeStepLimit = stepLimit
	s.columnReentryLimit = reentryLimit
}

// SetRoutingLimitsFromEnv reads NIUNIU_ROUTE_STEP_LIMIT / NIUNIU_COLUMN_REENTRY_LIMIT
// (positive integers; absent or invalid => package default) and applies them. Wired
// once at boot (server.New).
func (s *EpicExecutionService) SetRoutingLimitsFromEnv() {
	s.SetRoutingLimits(envPositiveInt("NIUNIU_ROUTE_STEP_LIMIT"), envPositiveInt("NIUNIU_COLUMN_REENTRY_LIMIT"))
}

// envPositiveInt parses a positive integer env var, returning 0 (=use default) when
// it is unset, blank, non-numeric, or <=0.
func envPositiveInt(key string) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func (s *EpicExecutionService) effectiveRouteStepLimit() int {
	if s.routeStepLimit <= 0 {
		return defaultRouteStepLimit
	}
	return s.routeStepLimit
}

func (s *EpicExecutionService) effectiveColumnReentryLimit() int {
	if s.columnReentryLimit <= 0 {
		return defaultColumnReentryLimit
	}
	return s.columnReentryLimit
}

// recordRouteVisit increments the (issue,column) visit counter and returns the
// column's new re-entry count and the issue's new total routing steps. The table
// is migrate-only/raw-SQL (not in sqlc); both `?` anchor to typed columns (PG-safe).
// ON CONFLICT upsert works on both SQLite (modernc) and Postgres.
func (s *EpicExecutionService) recordRouteVisit(ctx context.Context, issueID, columnID int64) (reentry, total int) {
	if s.db == nil {
		return 0, 0
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO issue_route_visits (issue_id, column_id, visit_count) VALUES (?, ?, 1)
		 ON CONFLICT(issue_id, column_id) DO UPDATE SET visit_count = visit_count + 1`,
		issueID, columnID); err != nil {
		slog.Warn("advance_issue: record route visit", "issueID", issueID, "columnID", columnID, "error", err)
		return 0, 0
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT visit_count FROM issue_route_visits WHERE issue_id = ? AND column_id = ?`,
		issueID, columnID).Scan(&reentry); err != nil {
		slog.Warn("advance_issue: read re-entry count", "issueID", issueID, "error", err)
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(visit_count), 0) FROM issue_route_visits WHERE issue_id = ?`,
		issueID).Scan(&total); err != nil {
		slog.Warn("advance_issue: read total route steps", "issueID", issueID, "error", err)
	}
	return reentry, total
}

// escalateRoutingBlocked lands an issue in the blocked-needs-human terminal state
// when a routing cap trips (§23.2/§19): it reuses stage 4's blocked semantics —
// exec_status='gate_blocked' (+reason) on the issue and the workspace conditionally
// flipped to 'attention' so it surfaces in the "需要我处理" view.
func (s *EpicExecutionService) escalateRoutingBlocked(ctx context.Context, issue store.Issue, reason string) {
	slog.Warn("advance_issue: routing livelock cap exceeded, escalating to blocked-needs-human",
		"issueID", issue.ID, "reason", reason)
	if err := s.q.SetIssueExecStatusWithReason(ctx, store.SetIssueExecStatusWithReasonParams{
		ExecStatus:       "gate_blocked",
		ExecStatusReason: sql.NullString{String: reason, Valid: true},
		ID:               issue.ID,
	}); err != nil {
		slog.Warn("advance_issue: set exec_status gate_blocked", "issueID", issue.ID, "error", err)
	}
	if s.execEvents != nil {
		s.execEvents.Record(ctx, ExecEvent{IssueID: issue.ID, Kind: "terminal", Summary: "blocked-needs-human：" + reason})
	}
	s.flipWorkspaceAttention(ctx, issue.ID)
}

// flipWorkspaceAttention conditionally flips an issue's active workspace to
// 'attention' from an active state (running/needs_review/created), so the issue
// surfaces in the "需要我处理" view. It never stomps a workspace the user has
// paused or that has already completed/archived.
func (s *EpicExecutionService) flipWorkspaceAttention(ctx context.Context, issueID int64) {
	ws, ok := s.activeWorkspaceForIssue(ctx, issueID)
	if !ok {
		return
	}
	for _, from := range []string{"running", "needs_review", "created"} {
		if err := s.q.UpdateWorkspaceStatusConditional(ctx, store.UpdateWorkspaceStatusConditionalParams{
			Status: "attention", ID: ws.ID, Status_2: from,
		}); err != nil {
			slog.Warn("flip workspace attention", "workspaceID", ws.ID, "from", from, "error", err)
		}
	}
}

// SetExecEventService wires the per-issue execution-timeline recorder (spec §23.7).
// Optional / nil-safe; wired once at boot (server.New).
func (s *EpicExecutionService) SetExecEventService(e *ExecEventService) { s.execEvents = e }

// SetCheckpointService wires the hidden-ref checkpoint recorder used by AdvanceIssue
// to snapshot the worktree when an issue enters the implement column. Optional /
// nil-safe; wired once at boot (server.New). See checkpoint.go.
func (s *EpicExecutionService) SetCheckpointService(c *CheckpointService) { s.checkpoints = c }

// AbandonIssueInput is the request for AbandonIssue (the abandon_issue MCP tool).
type AbandonIssueInput struct {
	IssueID      int64
	Reason       string // required: why the AI is abandoning (做不了/不该我做)
	CallerUserID int64
}

// AbandonIssueResult is the response for AbandonIssue.
type AbandonIssueResult struct {
	IssueID    int64  `json:"issue_id"`
	ColumnID   int64  `json:"column_id"`
	ColumnName string `json:"column_name"`
	Reason     string `json:"reason"`
}

// AbandonIssue is the abandoned-with-reason terminal transition (spec §19): the AI
// judged it cannot / should not do this issue. The card moves back to the project
// backlog (first op_primitive='none' column, else leftmost), exec_status becomes
// 'abandoned' with the reason recorded, and the workspace is flipped to 'attention'
// so the issue surfaces in the needs-attention view rather than silently sinking.
// No instruction is sent and no gate fires.
func (s *EpicExecutionService) AbandonIssue(ctx context.Context, in AbandonIssueInput) (AbandonIssueResult, error) {
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return AbandonIssueResult{}, errors.New("reason is required to abandon an issue")
	}
	if len(reason) > 4000 {
		reason = reason[:4000]
	}
	issue, err := s.q.GetIssue(ctx, in.IssueID)
	if err != nil {
		return AbandonIssueResult{}, fmt.Errorf("load issue %d: %w", in.IssueID, err)
	}
	srcCol, err := s.q.GetColumn(ctx, issue.ColumnID)
	if err != nil {
		return AbandonIssueResult{}, fmt.Errorf("load source column: %w", err)
	}
	backlogID, backlogName, err := s.backlogColumn(ctx, srcCol.ProjectID)
	if err != nil {
		return AbandonIssueResult{}, err
	}
	// AI-initiated move (MCP source): abandoning is the agent's own decision, so it
	// must not trip the §18 human last-writer interrupt against its own session.
	if err := s.kanban.MoveIssueWithSource(ctx, in.IssueID, backlogID, 0, in.CallerUserID, MoveSourceMCPAdvance); err != nil {
		return AbandonIssueResult{}, fmt.Errorf("move issue to backlog: %w", err)
	}
	if err := s.q.SetIssueExecStatusWithReason(ctx, store.SetIssueExecStatusWithReasonParams{
		ExecStatus:       "abandoned",
		ExecStatusReason: sql.NullString{String: reason, Valid: true},
		ID:               in.IssueID,
	}); err != nil {
		slog.Warn("abandon_issue: set exec_status abandoned", "issueID", in.IssueID, "error", err)
	}
	if s.execEvents != nil {
		s.execEvents.Record(ctx, ExecEvent{IssueID: in.IssueID, Kind: "terminal", Summary: "abandoned：" + reason})
	}
	s.auditIssueDecision(ctx, issue, in.CallerUserID, "issue.abandoned",
		resourceAuditPayload(issue.Title, "reason", reason))
	s.flipWorkspaceAttention(ctx, in.IssueID)
	slog.Info("abandon_issue: abandoned", "issueID", in.IssueID, "reason", reason)
	return AbandonIssueResult{IssueID: in.IssueID, ColumnID: backlogID, ColumnName: backlogName, Reason: reason}, nil
}

// backlogColumn resolves the project's parking column for an abandoned issue: the
// first op_primitive='none' column by position, else the leftmost column.
func (s *EpicExecutionService) backlogColumn(ctx context.Context, projectID int64) (int64, string, error) {
	cols, err := s.q.ListColumnsByProject(ctx, projectID)
	if err != nil {
		return 0, "", fmt.Errorf("list project columns: %w", err)
	}
	if len(cols) == 0 {
		return 0, "", fmt.Errorf("project %d has no columns", projectID)
	}
	for _, c := range cols { // ListColumnsByProject is position-ordered
		op, oerr := s.getColumnOp(ctx, c.ID)
		if oerr == nil && op.opPrimitive == "none" {
			return c.ID, c.Name, nil
		}
	}
	return cols[0].ID, cols[0].Name, nil
}

// ProjectIssueExecForWorkspace sets the linked issue's exec_status (+reason) for a
// workspace (spec §19, used by the ask_user waiting-user-input projection). A
// non-empty reason also flips the workspace to attention (a terminal escalation
// such as ask_user timeout); a plain status (e.g. restore to running) does not.
func (s *EpicExecutionService) ProjectIssueExecForWorkspace(ctx context.Context, workspaceID int64, status, reason string) {
	ws, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil || !ws.IssueID.Valid {
		return
	}
	if reason != "" {
		if err := s.q.SetIssueExecStatusWithReason(ctx, store.SetIssueExecStatusWithReasonParams{
			ExecStatus: status, ExecStatusReason: sql.NullString{String: reason, Valid: true}, ID: ws.IssueID.Int64,
		}); err != nil {
			slog.Warn("project issue exec (reason)", "workspaceID", workspaceID, "error", err)
		}
		s.flipWorkspaceAttention(ctx, ws.IssueID.Int64)
		return
	}
	if err := s.q.SetIssueExecStatus(ctx, store.SetIssueExecStatusParams{ExecStatus: status, ID: ws.IssueID.Int64}); err != nil {
		slog.Warn("project issue exec", "workspaceID", workspaceID, "error", err)
	}
}

// auditIssueDecision writes an org_audit_log entry for an AI- or human-driven key
// decision on an issue (spec §18): routing (advance_issue, incl. route-to-complete /
// skip / back), abandon, goal_condition change. Resolves the issue's org owner via its
// project; appendResourceAudit is a no-op for personal ('user') owners, so this is safe
// to call unconditionally. Best-effort — never blocks the decision path.
func (s *EpicExecutionService) auditIssueDecision(ctx context.Context, issue store.Issue, actorUserID int64, action, payloadJSON string) {
	col, err := s.q.GetColumn(ctx, issue.ColumnID)
	if err != nil {
		return
	}
	proj, err := s.q.GetProject(ctx, col.ProjectID)
	if err != nil {
		return
	}
	appendResourceAudit(ctx, s.db, nil, proj.OwnerType, proj.OwnerID, actorUserID, action, "issue", issue.ID, payloadJSON)
}

// reasonSuffix renders an optional move reason as a "：reason" suffix (empty when blank).
func reasonSuffix(r string) string {
	r = strings.TrimSpace(r)
	if r == "" {
		return ""
	}
	return "：" + r
}

// isStandaloneIssue reports whether an issue is neither an epic nor a child of one
// (i.e. the post-create hook leaves it to manual control).
func isStandaloneIssue(issue store.Issue) bool {
	_, managed := epicIDForIssue(issue)
	return !managed
}

// composeInstructMessage builds the `instruct` entry command (spec §4.1): the
// 【column】op_instruction header followed by the issue's title+description as
// context, so the agent knows both WHAT this lane asks for and WHICH issue it is.
// extra (may be empty) is appended verbatim — the review-loop (#623) uses it to
// weave the two-layer review context onto a review→implement bounce.
func composeInstructMessage(col columnOp, issue store.Issue, extra string) string {
	var b strings.Builder
	b.WriteString("【")
	b.WriteString(col.name)
	b.WriteString("】")
	if col.instruction != "" {
		b.WriteString(col.instruction)
	}
	b.WriteString("\n\n---\n\n")
	b.WriteString(composeIssueMarkdown(issue.Title, nullStringVal(issue.Description)))
	if strings.TrimSpace(extra) != "" {
		b.WriteString(extra)
	}
	return b.String()
}
