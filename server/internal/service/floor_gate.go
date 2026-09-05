package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/git"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// Column-native floor gate (AI-native board execution, stage 4; spec
// 2026-06-05-ai-native-board-execution-design.md §5.3/§11.3/§22/§23.3).
//
// The floor gate is the "底线闸": the set of harness specs a project binds with
// applicability='always' (via column_gate_specs). Unlike the legacy template-exit
// gate (gate_runner.go, keyed by harness_run.template_id), the floor gate is
// column-native — it is resolved from the project's column_gate_specs directly,
// runs at the single completion choke point (RequestWorkspaceCompletion), and has
// no harness_run / template_id at all (spec §5 P0: "新建一条 column-native gate 调度
// 路径，不是复用"). State is carried on issue.exec_status (§11.3 option ①), not on a
// harness_run row.
//
// It is intentionally an in-process async runner (a detached goroutine per request)
// rather than a gate_jobs row: gate_jobs.run_id is NOT NULL and sqlc-typed into the
// live template path, so generalizing it would ripple through generated code for no
// benefit here. The floor gate reuses only the spec executor (GateSpecExecutor) and
// the gate event types.

// CompletionTrigger identifies who asked to complete a workspace. It selects how a
// failed floor gate is handled (§22.3): a human mark-done leaves the failure for the
// user to act on; an automatic completion (autohost judge-stop / epic OnAgentDone)
// re-engages the agent to self-fix, bounded by floor_retry_count.
type CompletionTrigger string

const (
	TriggerHuman CompletionTrigger = "human"
	TriggerAuto  CompletionTrigger = "auto"
)

// CompletionStatus is the synchronous outcome of RequestWorkspaceCompletion. The
// gate itself is async, so a workspace with a non-empty floor gate returns
// CompletionGateChecking immediately and resolves later via the gate callback.
type CompletionStatus string

const (
	CompletionFinalized    CompletionStatus = "finalized"
	CompletionGateChecking CompletionStatus = "gate_checking"
)

// RequestCompletionResult is the synchronous result of RequestWorkspaceCompletion.
type RequestCompletionResult struct {
	Status CompletionStatus `json:"status"`
	// Warnings is carried from finalize (e.g. issue lifecycle sync failed) only when
	// the workspace finalized synchronously (no floor gate).
	Warnings []string `json:"warnings,omitempty"`
}

// FloorRetryKicker re-engages a workspace's autohost agent to self-fix a failed
// floor gate (§22.3 auto path: "失败回灌 agent 重新唤起"). Optional; nil-safe — with
// no kicker wired an auto failure escalates straight to attention instead of
// self-fixing, which still satisfies the "绝不无限循环" guarantee.
type FloorRetryKicker interface {
	KickFloorRetry(ctx context.Context, workspaceID int64, failures []GateFailure)
}

const (
	defaultFloorRetryLimit = 3
	floorSpecJobTimeout    = 10 * time.Minute
)

// floorSpec is one resolved floor gate check with the severity that decides whether
// its failure blocks (§5.1: only severity='error' blocks; warning/info are advisory)
// and code_probe_only, which marks it build/test-class so it is auto-N/A'd for a
// no-code-diff (doc/research) issue (§23.6).
//
// specID == 0 identifies the project's 底线 command (projects.floor_command) rather
// than a harness_specs row: the P1 single-field floor. It carries its command inline
// and is always error-severity + code-probe-class (a build/test command is exactly
// what §23.6 means by code-class).
type floorSpec struct {
	specID        int64
	severity      string
	codeProbeOnly bool
	// command is set only for the project floor command (specID == 0).
	command    string
	timeoutSec int
}

// isProjectFloorCommand reports whether this entry is the project's floor_command
// rather than a harness_specs-backed spec.
func (f floorSpec) isProjectFloorCommand() bool { return f.specID == 0 }

// runFloorCheck executes one floor entry against one repo path, routing the
// project 底线 command to ExecuteCommand and everything else to ExecuteSpec.
// Shared by the floor gate and the column exit gate so both treat the two
// sources identically.
func runFloorCheck(ctx context.Context, exec GateSpecExecutor, sp floorSpec, repoPath string) (bool, string, error) {
	if sp.isProjectFloorCommand() {
		return exec.ExecuteCommand(ctx, sp.command, sp.timeoutSec, repoPath)
	}
	return exec.ExecuteSpec(ctx, 0, sp.specID, repoPath)
}

// SetFloorGateDeps wires the dependencies the column-native floor gate needs.
// Called once at boot (server.New). All are optional/nil-safe:
//   - db: raw *store.DB for the applicability + floor_retry_count reads/writes that
//     are not modelled in sqlc (stage-1a/4 convention).
//   - gateExec: the spec executor (harness CheckRunner adapter). nil => no floor gate
//     ever runs (every completion finalizes directly), used by tests that don't care.
//   - kicker: autohost re-engage on auto failure. nil => escalate to attention.
//   - retryLimit: max auto self-fix rounds before escalating (<=0 => default 3).
func (s *WorkspaceOpsService) SetFloorGateDeps(db *sql.DB, gateExec GateSpecExecutor, kicker FloorRetryKicker, retryLimit int) {
	s.db = store.Wrap(db)
	s.gateExec = gateExec
	s.floorKicker = kicker
	s.floorRetryLimit = retryLimit
}

func (s *WorkspaceOpsService) retryLimit() int {
	if s.floorRetryLimit <= 0 {
		return defaultFloorRetryLimit
	}
	return s.floorRetryLimit
}

// RequestWorkspaceCompletion is the single entry point for completing a workspace
// (spec §22.2). It runs the project's floor gate (applicability='always' specs) and
// only finalizes (flips status='completed') when the底线 is green; on a blocking
// failure it routes by trigger (§22.3) and never deadlocks.
//
// Returns synchronously:
//   - {finalized}  when there is no floor gate to run (finalize happened inline), or
//     the workspace was already completed (idempotent).
//   - {gate_checking} when a floor gate was enqueued; the terminal outcome (finalize
//     or block) lands asynchronously via the gate callback and is observable on
//     issue.exec_status ('gate_checking' -> 'done' | 'gate_blocked').
//
// Errors: ErrWorkspaceRunning if the agent is still running (stop it first).
func (s *WorkspaceOpsService) RequestWorkspaceCompletion(ctx context.Context, workspaceID int64, trigger CompletionTrigger) (RequestCompletionResult, error) {
	ws, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return RequestCompletionResult{}, fmt.Errorf("workspace not found: %w", err)
	}
	if ws.Status == "running" {
		return RequestCompletionResult{}, ErrWorkspaceRunning
	}
	if ws.Status == "completed" {
		return RequestCompletionResult{Status: CompletionFinalized}, nil // idempotent
	}

	specs := s.listFloorSpecs(ctx, ws)
	// No floor gate (no specs, no executor wired, or no linked issue/project): finalize
	// directly. The底线 promise is vacuously satisfied — there is no error-severity
	// always-spec to clear.
	if len(specs) == 0 || s.gateExec == nil {
		res, ferr := s.finalizeCompletion(ctx, ws)
		if ferr != nil {
			return RequestCompletionResult{}, ferr
		}
		return RequestCompletionResult{Status: CompletionFinalized, Warnings: res.Warnings}, nil
	}

	// A floor gate is already in flight for this workspace (e.g. a duplicate trigger):
	// don't double-run; the existing gate's callback will resolve completion.
	if !s.beginFloorGate(workspaceID) {
		return RequestCompletionResult{Status: CompletionGateChecking}, nil
	}
	if ws.IssueID.Valid {
		if err := s.q.SetIssueExecStatus(ctx, store.SetIssueExecStatusParams{
			ExecStatus: "gate_checking", ID: ws.IssueID.Int64,
		}); err != nil {
			slog.Warn("floor gate: set exec_status gate_checking", "workspaceID", workspaceID, "error", err)
		}
	}
	s.recordIssueExec(ctx, ws, "gate", "底线闸: 开始检查")
	// Detached context: the request ctx is cancelled when the handler returns, but the
	// gate (build/test subprocesses) outlives it. Per-spec timeouts bound each run.
	go s.runFloorGate(context.WithoutCancel(ctx), ws, specs, trigger)
	return RequestCompletionResult{Status: CompletionGateChecking}, nil
}

// listFloorSpecs resolves the workspace's project's floor gate. It is the union of:
//
//   - the project's 底线 command (projects.floor_command), when set — the P1
//     single-field floor, carried as a floorSpec with specID 0; and
//   - the DISTINCT set of enabled harness specs bound with applicability='always'
//     on ANY column of the project (the底线 is project-level, §11.2) — the advanced
//     path, for users who want more than one condition.
//
// Returns nil when the workspace has no linked issue/project and neither source
// yields anything.
//
// floor_command / applicability are migrate-only (not in sqlc), so these are raw
// SQL. project_id anchors the only bound parameter to a typed column, so it is
// PG-safe (no 42P18).
func (s *WorkspaceOpsService) listFloorSpecs(ctx context.Context, ws store.Workspace) []floorSpec {
	if s.db == nil || !ws.IssueID.Valid {
		return nil
	}
	issue, err := s.q.GetIssue(ctx, ws.IssueID.Int64)
	if err != nil {
		slog.Warn("floor gate: load issue", "workspaceID", ws.ID, "error", err)
		return nil
	}
	col, err := s.q.GetColumn(ctx, issue.ColumnID)
	if err != nil {
		slog.Warn("floor gate: load column", "workspaceID", ws.ID, "error", err)
		return nil
	}

	var specs []floorSpec

	// The project 底线 command comes first so it is the first thing evaluated (and,
	// with fail-fast, the failure a user most likely wants reported).
	var floorCmd string
	var floorTimeout int
	if err := s.db.QueryRowContext(ctx,
		`SELECT floor_command, floor_timeout_sec FROM projects WHERE id = ?`,
		col.ProjectID).Scan(&floorCmd, &floorTimeout); err != nil {
		slog.Warn("floor gate: read project floor_command", "projectID", col.ProjectID, "error", err)
	} else if strings.TrimSpace(floorCmd) != "" {
		specs = append(specs, floorSpec{
			specID:        0,
			severity:      "error", // the floor is by definition blocking
			codeProbeOnly: true,    // a build/test command is code-class (§23.6)
			command:       floorCmd,
			timeoutSec:    floorTimeout,
		})
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT cgs.spec_id, hs.severity, hs.code_probe_only
		FROM column_gate_specs cgs
		JOIN columns c ON c.id = cgs.column_id
		JOIN harness_specs hs ON hs.id = cgs.spec_id
		WHERE c.project_id = ? AND cgs.applicability = 'always' AND hs.enabled = 1`,
		col.ProjectID)
	if err != nil {
		slog.Warn("floor gate: list floor specs", "projectID", col.ProjectID, "error", err)
		return specs
	}
	defer rows.Close()
	for rows.Next() {
		var fs floorSpec
		var codeProbe int64
		if err := rows.Scan(&fs.specID, &fs.severity, &codeProbe); err != nil {
			slog.Warn("floor gate: scan floor spec", "error", err)
			return specs
		}
		fs.codeProbeOnly = codeProbe != 0
		specs = append(specs, fs)
	}
	return specs
}

// runFloorGate executes the floor specs across every repo worktree of the workspace
// and aggregates the result (§23.3: any repo's severity=error failure = overall
// block). It then dispatches the outcome via onFloorGateDone. Runs in its own
// goroutine; a panic here must not take down the server (matches the agentproxy
// background-goroutine safety net).
func (s *WorkspaceOpsService) runFloorGate(ctx context.Context, ws store.Workspace, specs []floorSpec, trigger CompletionTrigger) {
	defer s.endFloorGate(ws.ID)
	defer func() {
		if r := recover(); r != nil {
			slog.Error("floor gate: panic recovered", "workspaceID", ws.ID, "panic", r, "stack", string(debug.Stack()))
			// Treat a crashed gate as a block so completion does not silently finalize.
			s.onFloorGateDone(context.Background(), ws, false, []GateFailure{{Reason: "executor_error", Output: fmt.Sprintf("floor gate panicked: %v", r)}}, trigger)
		}
	}()

	paths := s.worktreePaths(ctx, ws)

	// §23.6 output probing: classify what this workspace produced so build/test-class
	// floor specs (code_probe_only) are not run against a pure doc/research issue.
	codeDiff, anyOutput := s.probeOutput(ctx, ws)
	var runSpecs []floorSpec
	naCount := 0
	ranNonCodeFloor := false
	for _, sp := range specs {
		if sp.codeProbeOnly && !codeDiff {
			// No code diff: a build/test-class floor is N/A (neither fail nor silently
			// pass). It is simply not executed; the non-code兜底 below ensures the issue
			// still has a completion门槛.
			naCount++
			slog.Info("floor gate: code-class spec N/A (no code diff)",
				"workspaceID", ws.ID, "specID", sp.specID)
			continue
		}
		if !sp.codeProbeOnly {
			ranNonCodeFloor = true
		}
		runSpecs = append(runSpecs, sp)
	}

	total := len(runSpecs) * len(paths)
	idx := 0
	passed := true
	var failures []GateFailure

specLoop:
	for _, sp := range runSpecs {
		for _, p := range paths {
			jobCtx, cancel := context.WithTimeout(ctx, floorSpecJobTimeout)
			ok, output, execErr := runFloorCheck(jobCtx, s.gateExec, sp, p)
			cancel()
			idx++
			s.publishFloorProgress(ws, sp.specID, idx, total, ok && execErr == nil)

			if ok && execErr == nil {
				continue
			}
			// Only error-severity failures block (§5.1); warning/info are advisory and
			// recorded-but-passed ("仅 warning/info 失败 -> 记录放行").
			if sp.severity != "error" {
				slog.Info("floor gate: advisory spec failed (non-blocking)",
					"workspaceID", ws.ID, "specID", sp.specID, "repo", p, "severity", sp.severity)
				continue
			}
			passed = false
			failures = append(failures, GateFailure{
				SpecID: sp.specID,
				Output: truncateStr(floorFailureOutput(p, output, execErr), 4096),
				Reason: floorFailureReason(execErr),
			})
			break specLoop // fail-fast: one error-severity failure in one repo blocks all
		}
	}

	// §23.6 non-code floor兜底: a no-code-diff issue whose floor had ONLY code-class
	// specs (all N/A'd) would otherwise finalize with no threshold at all
	// ("N/A=底线失效"). The minimal non-code门槛 is "产出非空": the workspace must have
	// produced something (a doc/research issue creates new files even with no code diff).
	// If it produced nothing at all, block. When the project binds a real non-code floor
	// spec (code_probe_only=0) it runs above and provides the门槛, so this only fires
	// when there is no non-code floor to fall back on.
	if passed && !codeDiff && naCount > 0 && !ranNonCodeFloor && !anyOutput {
		passed = false
		failures = append(failures, GateFailure{
			Reason: "no_output",
			Output: "底线(产出非空): 无代码 diff 且工作区未产出任何文件",
		})
		slog.Info("floor gate: non-code产出非空兜底 blocked (empty workspace)", "workspaceID", ws.ID)
	}

	s.publishFloorDone(ws, passed, failures)
	s.onFloorGateDone(ctx, ws, passed, failures, trigger)
}

// summarizeGateFailures renders a short human-readable reason from the failing
// gate checks, for the gate_blocked card chip + timeline. SpecID 0 is the project's
// 底线 command, which has no spec row to name.
func summarizeGateFailures(failures []GateFailure) string {
	if len(failures) == 0 {
		return "底线未通过"
	}
	parts := make([]string, 0, len(failures))
	for _, f := range failures {
		if f.SpecID == 0 && f.Reason != "no_output" {
			parts = append(parts, fmt.Sprintf("底线命令(%s)", f.Reason))
			continue
		}
		if f.SpecID == 0 {
			parts = append(parts, f.Reason)
			continue
		}
		parts = append(parts, fmt.Sprintf("spec#%d(%s)", f.SpecID, f.Reason))
	}
	return strings.Join(parts, ", ")
}

// probeOutput classifies what a workspace produced across all its repo worktrees
// (spec §23.6). codeDiff is true when any worktree has uncommitted tracked changes
// (git diff HEAD — the workspace's code output, which niuniu reviews before commit).
// anyOutput is true when any worktree has ANY change including new untracked files
// (git status --porcelain), the signal a doc/research issue produced something.
//
// Fail-SAFE: when a worktree cannot be probed (not a git repo, git missing), assume
// both true so the build/test floors run exactly as before — N/A only ever fires on a
// definite no-diff. This keeps non-git test workspaces and odd setups on the prior path.
func (s *WorkspaceOpsService) probeOutput(ctx context.Context, ws store.Workspace) (codeDiff, anyOutput bool) {
	for _, p := range s.worktreePaths(ctx, ws) {
		diffs, err := git.Diff(p, "")
		if err != nil {
			return true, true // cannot classify -> conservative: run the floors
		}
		if len(diffs) > 0 {
			return true, true // a code diff implies output too
		}
		if !anyOutput {
			entries, serr := git.Status(p)
			if serr != nil {
				return true, true
			}
			if len(entries) > 0 {
				anyOutput = true
			}
		}
	}
	return false, anyOutput
}

// onFloorGateDone collects the gate result. Green -> finalize. Blocked -> land
// exec_status='gate_blocked' and route by trigger (§22.3), never deadlocking.
func (s *WorkspaceOpsService) onFloorGateDone(ctx context.Context, ws store.Workspace, passed bool, failures []GateFailure, trigger CompletionTrigger) {
	if passed {
		if ws.IssueID.Valid {
			if err := s.q.SetIssueExecStatus(ctx, store.SetIssueExecStatusParams{ExecStatus: "done", ID: ws.IssueID.Int64}); err != nil {
				slog.Warn("floor gate: set exec_status done", "workspaceID", ws.ID, "error", err)
			}
		}
		s.recordIssueExec(ctx, ws, "gate", "底线闸: 通过")
		// Autohost 安全网: snapshot the gate-passing state so a later gate failure
		// can rewind to this known-good baseline. Best-effort; before finalize so a
		// finalize hiccup does not lose the checkpoint.
		if s.checkpoints != nil && ws.IssueID.Valid {
			if _, err := s.checkpoints.Snapshot(ctx, ws.IssueID.Int64, ws.ID,
				CheckpointKindGatePass, "底线闸通过", CheckpointGateStatusPass); err != nil {
				slog.Warn("floor gate: checkpoint-on-pass failed", "workspaceID", ws.ID, "error", err)
			}
		}
		if _, err := s.finalizeCompletion(ctx, ws); err != nil {
			slog.Error("floor gate: finalize after pass", "workspaceID", ws.ID, "error", err)
		}
		return
	}

	// Blocked: land the truthful blocked state on the issue (§11.3 option ①) so the
	// card chip reflects it, recording the failing-spec reason. Workspace NOT completed.
	blockReason := summarizeGateFailures(failures)
	if ws.IssueID.Valid {
		if err := s.q.SetIssueExecStatusWithReason(ctx, store.SetIssueExecStatusWithReasonParams{
			ExecStatus: "gate_blocked", ExecStatusReason: sql.NullString{String: blockReason, Valid: true}, ID: ws.IssueID.Int64,
		}); err != nil {
			slog.Warn("floor gate: set exec_status gate_blocked", "workspaceID", ws.ID, "error", err)
		}
	}
	s.recordIssueExec(ctx, ws, "gate", "底线闸: 阻断 - "+blockReason+gateFailureOutputTail(failures))
	slog.Info("floor gate: blocked", "workspaceID", ws.ID, "trigger", trigger, "failures", len(failures))

	switch trigger {
	case TriggerAuto:
		// Auto path: re-engage the agent to self-fix, bounded by floor_retry_count.
		// Over the limit -> escalate to attention (§19 blocked-needs-human); never loop.
		n := s.incrFloorRetry(ctx, ws.IssueID)
		if n <= s.retryLimit() && s.floorKicker != nil {
			slog.Info("floor gate: auto self-fix kick", "workspaceID", ws.ID, "attempt", n, "limit", s.retryLimit())
			// Autohost 安全网: before re-engaging, rewind the worktree to the last
			// gate-passing checkpoint so the agent self-fixes from a known-good
			// baseline instead of on top of the failing changes (precise回退 vs the
			// old whole-column rollback). Best-effort; a missing baseline just leaves
			// the current state and still re-engages.
			if s.checkpoints != nil && ws.IssueID.Valid {
				if step, ok, rerr := s.checkpoints.RevertToLastPassing(ctx, ws.IssueID.Int64); rerr != nil {
					slog.Warn("floor gate: revert-to-last-passing failed", "workspaceID", ws.ID, "error", rerr)
				} else if ok {
					slog.Info("floor gate: reverted to last passing checkpoint before self-fix", "workspaceID", ws.ID, "step", step)
					s.recordIssueExec(ctx, ws, "gate", fmt.Sprintf("底线闸失败: 已回退到通过 checkpoint(step %d) 续跑", step))
				}
			}
			s.floorKicker.KickFloorRetry(ctx, ws.ID, failures)
			return
		}
		s.escalateBlocked(ctx, ws, n)
	case TriggerHuman:
		// Human path: leave the workspace status (needs_review/attention) as-is; the SPA
		// reads exec_status='gate_blocked' + the gate_done(failures) event and renders
		// "底线未过", offering "让 AI 修" / 人工修. No auto retry.
	}
}

// escalateBlocked flips a stuck auto-completion workspace to 'attention' so it
// surfaces in the "需要我处理" view (§19 blocked-needs-human). The auto path reaches
// here from OnAgentDone, which set the workspace to 'needs_review' just before; the
// conditional flip only fires from needs_review, so it never stomps a workspace the
// user has since taken over (paused) or that an agent has resumed (running).
func (s *WorkspaceOpsService) escalateBlocked(ctx context.Context, ws store.Workspace, attempts int) {
	slog.Warn("floor gate: auto self-fix exhausted, escalating to attention",
		"workspaceID", ws.ID, "attempts", attempts, "limit", s.retryLimit())
	if err := s.q.UpdateWorkspaceStatusConditional(ctx, store.UpdateWorkspaceStatusConditionalParams{
		Status: "attention", ID: ws.ID, Status_2: "needs_review",
	}); err != nil {
		slog.Warn("floor gate: escalate set attention", "workspaceID", ws.ID, "error", err)
	}
}

// worktreePaths returns one filesystem path per repo worktree of the workspace, for
// per-repo floor gate execution (§23.3). Falls back to the workspace path when no
// worktree groups resolve (tests / single-dir workspaces) so the gate still runs once.
func (s *WorkspaceOpsService) worktreePaths(ctx context.Context, ws store.Workspace) []string {
	if s.ws != nil {
		if groups, err := s.ws.ListWorktreeGroups(ctx, ws.ID); err == nil && len(groups) > 0 {
			paths := make([]string, 0, len(groups))
			for _, g := range groups {
				paths = append(paths, g.Path)
			}
			return paths
		}
	}
	return []string{ws.Path}
}

// incrFloorRetry atomically increments and returns the issue's floor_retry_count.
// Returns 0 when there is no linked issue (nothing to bound; caller escalates).
// floor_retry_count is migrate-only (not in sqlc) -> anchored raw SQL.
func (s *WorkspaceOpsService) incrFloorRetry(ctx context.Context, issueID sql.NullInt64) int {
	if s.db == nil || !issueID.Valid {
		return 0
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE issues SET floor_retry_count = floor_retry_count + 1, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		issueID.Int64); err != nil {
		slog.Warn("floor gate: incr floor_retry_count", "issueID", issueID.Int64, "error", err)
		return 0
	}
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT floor_retry_count FROM issues WHERE id = ?`, issueID.Int64).Scan(&n); err != nil {
		slog.Warn("floor gate: read floor_retry_count", "issueID", issueID.Int64, "error", err)
		return 0
	}
	return n
}

// beginFloorGate marks a workspace as having an in-flight floor gate; returns false
// if one is already running (caller should not double-run).
func (s *WorkspaceOpsService) beginFloorGate(workspaceID int64) bool {
	s.floorMu.Lock()
	defer s.floorMu.Unlock()
	if s.floorInFlight == nil {
		s.floorInFlight = make(map[int64]struct{})
	}
	if _, ok := s.floorInFlight[workspaceID]; ok {
		return false
	}
	s.floorInFlight[workspaceID] = struct{}{}
	return true
}

func (s *WorkspaceOpsService) endFloorGate(workspaceID int64) {
	s.floorMu.Lock()
	defer s.floorMu.Unlock()
	delete(s.floorInFlight, workspaceID)
}

// RecoverFloorGates收口 issues left in 'gate_checking' by a crash mid-gate (the
// in-flight goroutine is gone). They are reset to a safe, user-visible state:
// exec_status -> 'gate_blocked' and the workspace -> 'attention', so a stuck
// completion never sits invisible (§19). Called once at startup. Best-effort.
func (s *WorkspaceOpsService) RecoverFloorGates(ctx context.Context) {
	if s.db == nil {
		return
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT w.id FROM workspaces w
		JOIN issues i ON i.id = w.issue_id
		WHERE i.exec_status = 'gate_checking'`)
	if err != nil {
		slog.Warn("floor gate recovery: query", "error", err)
		return
	}
	defer rows.Close()
	var wsIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			wsIDs = append(wsIDs, id)
		}
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE issues SET exec_status = 'gate_blocked', updated_at = CURRENT_TIMESTAMP
		 WHERE exec_status = 'gate_checking'`); err != nil {
		slog.Warn("floor gate recovery: reset exec_status", "error", err)
	}
	for _, id := range wsIDs {
		if err := s.q.UpdateWorkspaceStatusConditional(ctx, store.UpdateWorkspaceStatusConditionalParams{
			Status: "attention", ID: id, Status_2: "needs_review",
		}); err != nil {
			slog.Warn("floor gate recovery: set attention", "workspaceID", id, "error", err)
		}
	}
	if len(wsIDs) > 0 {
		slog.Info("floor gate recovery: reset stuck gate_checking issues", "count", len(wsIDs))
	}
}

// ---------------------------------------------------------------------------
// Event helpers (nil-safe). Floor gate events carry JobID=0/RunID=0 (column-native,
// no harness_run) and identify the workspace via OutputEvent.WorkspaceId.
// ---------------------------------------------------------------------------

func (s *WorkspaceOpsService) publishFloorProgress(ws store.Workspace, specID int64, index, total int, passed bool) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(event.OutputEvent{
		Type:        event.EventGateProgress,
		Role:        "system",
		Ts:          time.Now().UnixMilli(),
		WorkspaceId: ws.ID,
		GateProgress: &event.GateProgressPayload{
			SpecID: specID, Index: index, Total: total, Passed: passed,
		},
	})
}

// publishFloorDone emits gate_done, carrying each blocking check's captured output
// so the UI can show WHY the gate blocked rather than only that it did.
func (s *WorkspaceOpsService) publishFloorDone(ws store.Workspace, passed bool, failures []GateFailure) {
	if s.bus == nil {
		return
	}
	details := make([]event.GateFailureDetail, 0, len(failures))
	for _, f := range failures {
		details = append(details, event.GateFailureDetail{
			SpecID: f.SpecID,
			Name:   gateFailureName(f),
			Reason: f.Reason,
			Output: f.Output,
		})
	}
	s.bus.Publish(event.OutputEvent{
		Type:        event.EventGateDone,
		Role:        "system",
		Ts:          time.Now().UnixMilli(),
		WorkspaceId: ws.ID,
		GateDone: &event.GateDonePayload{
			Passed: passed, FailureCount: len(failures), Failures: details,
		},
	})
}

// gateFailureOutputTail renders the first failing check's captured output as a
// short appendix for the issue timeline, so the persisted record answers "why"
// and not just "blocked". The live gate_done event carries the full set; this is
// the durable breadcrumb after the SSE stream is gone. Capped tight — the
// timeline is a summary surface, not a log viewer.
func gateFailureOutputTail(failures []GateFailure) string {
	for _, f := range failures {
		out := strings.TrimSpace(f.Output)
		if out == "" {
			continue
		}
		return "\n" + gateFailureName(f) + ": " + truncateStr(out, 600)
	}
	return ""
}

// gateFailureName labels a failing check for display. SpecID 0 is the project's
// 底线 command (no spec row to name); the 产出非空 fallback is its own case.
func gateFailureName(f GateFailure) string {
	if f.SpecID != 0 {
		return fmt.Sprintf("spec#%d", f.SpecID)
	}
	if f.Reason == "no_output" {
		return "底线(产出非空)"
	}
	return "底线命令"
}

// floorFailureOutput tags gate output with the repo path it came from so a multi-repo
// block names the offending repo (§23.3).
func floorFailureOutput(repoPath, output string, execErr error) string {
	prefix := "[repo " + repoPath + "] "
	if execErr != nil && errors.Is(execErr, context.DeadlineExceeded) {
		return prefix + "timed out\n" + output
	}
	if execErr != nil {
		return prefix + execErr.Error() + "\n" + output
	}
	return prefix + output
}

func floorFailureReason(execErr error) string {
	if execErr == nil {
		return "exit_nonzero"
	}
	if errors.Is(execErr, context.DeadlineExceeded) {
		return "timeout"
	}
	return "executor_error"
}
