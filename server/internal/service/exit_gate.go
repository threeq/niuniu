package service

import (
	"context"
	"database/sql"
	"log/slog"
	"runtime/debug"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// Column-native exit gate (workflow decommission, Plan 2). The exit gate runs a
// source column's applicability='if_routed' specs when an issue leaves that column
// (MoveIssueRunAware branch1). Unlike the retired template-exit gate (gate_runner.go
// + gate_jobs, keyed by harness_run), it is issue-bound and reuses the floor-gate
// column-native machinery (worktreePaths + GateSpecExecutor + issue exec_status +
// gate SSE). On a blocking failure it records the block on the issue timeline +
// exec_status and never rolls back the move — it is 事后质量反馈; whether to rework is
// left to the AI/human (consistent with the AI-native board's autonomous routing).

// RunExitGate runs the exit gate for issueID against srcColumnID's if_routed specs.
// No-op when the column has no if_routed specs, no executor is wired, or the issue
// has no active workspace. Called in a detached goroutine from MoveIssueRunAware;
// panics are recovered so a gate crash never takes down the server.
func (s *WorkspaceOpsService) RunExitGate(ctx context.Context, issueID, srcColumnID int64) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("exit gate: panic recovered", "issueID", issueID, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	if s.gateExec == nil || s.db == nil {
		return
	}
	specs := s.listExitSpecs(ctx, srcColumnID)
	if len(specs) == 0 {
		return
	}
	ws, ok := s.activeWorkspaceForIssueOps(ctx, issueID)
	if !ok {
		return
	}
	s.recordIssueExec(ctx, ws, "gate", "列出口闸: 开始检查")

	paths := s.worktreePaths(ctx, ws)
	total := len(specs) * len(paths)
	idx := 0
	passed := true
	var failures []GateFailure

specLoop:
	for _, sp := range specs {
		for _, p := range paths {
			jobCtx, cancel := context.WithTimeout(ctx, floorSpecJobTimeout)
			gOK, output, execErr := s.gateExec.ExecuteSpec(jobCtx, 0, sp.specID, p)
			cancel()
			idx++
			s.publishFloorProgress(ws, sp.specID, idx, total, gOK && execErr == nil)

			if gOK && execErr == nil {
				continue
			}
			// Only error-severity failures block (consistent with floor gate §5.1);
			// warning/info are advisory and recorded-but-passed.
			if sp.severity != "error" {
				slog.Info("exit gate: advisory spec failed (non-blocking)",
					"issueID", issueID, "specID", sp.specID, "repo", p, "severity", sp.severity)
				continue
			}
			passed = false
			failures = append(failures, GateFailure{
				SpecID: sp.specID,
				Output: truncateStr(floorFailureOutput(p, output, execErr), 4096),
				Reason: floorFailureReason(execErr),
			})
			break specLoop // fail-fast: one error-severity failure blocks
		}
	}

	s.publishFloorDone(ws, passed, len(failures))
	if passed {
		s.recordIssueExec(ctx, ws, "gate", "列出口闸: 通过")
		return
	}

	// Blocked: record the truthful blocked state on the issue (exec_status + reason)
	// and the timeline. Do NOT roll back the move; the AI/human decides whether to
	// rework based on the gate result.
	reason := summarizeGateFailures(failures)
	if ws.IssueID.Valid {
		if err := s.q.SetIssueExecStatusWithReason(ctx, store.SetIssueExecStatusWithReasonParams{
			ExecStatus:       "gate_blocked",
			ExecStatusReason: sql.NullString{String: reason, Valid: true},
			ID:               ws.IssueID.Int64,
		}); err != nil {
			slog.Warn("exit gate: set exec_status gate_blocked", "issueID", issueID, "error", err)
		}
	}
	s.recordIssueExec(ctx, ws, "gate", "列出口闸: 阻断 - "+reason)
	slog.Info("exit gate: blocked", "issueID", issueID, "srcColumn", srcColumnID, "failures", len(failures))
}

// listExitSpecs resolves srcColumnID's enabled applicability='if_routed' specs.
// applicability is migrate-only (not in sqlc), so this is raw SQL. column_id anchors
// the only bound parameter to a typed column (PG-safe, no 42P18).
func (s *WorkspaceOpsService) listExitSpecs(ctx context.Context, columnID int64) []floorSpec {
	rows, err := s.db.QueryContext(ctx, `
		SELECT cgs.spec_id, hs.severity, hs.code_probe_only
		FROM column_gate_specs cgs
		JOIN harness_specs hs ON hs.id = cgs.spec_id
		WHERE cgs.column_id = ? AND cgs.applicability = 'if_routed' AND hs.enabled = 1`,
		columnID)
	if err != nil {
		slog.Warn("exit gate: list specs", "columnID", columnID, "error", err)
		return nil
	}
	defer rows.Close()
	var specs []floorSpec
	for rows.Next() {
		var fs floorSpec
		var codeProbe int64
		if err := rows.Scan(&fs.specID, &fs.severity, &codeProbe); err != nil {
			slog.Warn("exit gate: scan spec", "error", err)
			return nil
		}
		fs.codeProbeOnly = codeProbe != 0
		specs = append(specs, fs)
	}
	return specs
}

// activeWorkspaceForIssueOps returns the issue's non-archived workspace (1:1).
func (s *WorkspaceOpsService) activeWorkspaceForIssueOps(ctx context.Context, issueID int64) (store.Workspace, bool) {
	wss, err := s.q.GetWorkspacesByIssue(ctx, sql.NullInt64{Int64: issueID, Valid: true})
	if err != nil {
		return store.Workspace{}, false
	}
	for _, w := range wss {
		if w.IsArchived == 0 {
			return w, true
		}
	}
	return store.Workspace{}, false
}
