package agentproxy

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// autohostBgPollInterval is how long the autohost watchdog waits before
// re-checking when the workspace has pending background work (a Bash[bg] /
// Subagent) but the agent did NOT set its own wakeup cadence. When the agent did
// schedule a wakeup, that time is used instead (see scheduleBgWaitResume).
const autohostBgPollInterval = 90 * time.Second

// autohostMinResumeDelay floors the paced bg-wait resume time so the current
// SendLoop has unwound to idle (StatusRunning -> idle) before the scheduler
// fires the resume. Without this, a near-term agent wakeup could schedule a
// resume that fires while the session is still running, which the scheduler
// trigger() skips (one-shot, not re-armed) -> lost resume -> stranded running.
const autohostMinResumeDelay = 15 * time.Second

// Autohost (自动托管) is niuniu's "let it run" permission mode. At the Claude
// CLI level it is identical to bypassPermissions (no permission-prompt-tool,
// no allowedTools); on top of that, SendLoop runs a watchdog that auto-injects
// a "continue" turn whenever the queue drains and the agent has not explicitly
// declared the task done. The intent is to remove the human-confirmation
// hops that otherwise pile up across a single task.

// autohostDefaultBudget bounds consecutive auto-continues per user-initiated
// chain. Tunable via NIUNIU_AUTOHOST_BUDGET env (workspace-level). Set 0 to
// disable auto-continue while keeping bypassPermissions behavior.
const autohostDefaultBudget = 12

// autohostDefaultErrorBudget bounds consecutive auto-recovery attempts per
// error streak. Tunable via NIUNIU_AUTOHOST_ERROR_BUDGET. Set 0 to disable
// error recovery — agent will stop on the first error.
const autohostDefaultErrorBudget = 3

// autohostContinuePrompt is the message the watchdog injects when the queue
// is empty AND no completion condition is configured. Phrased to (a) push the
// agent to keep going on the same task, (b) make decisions without asking,
// (c) emit the stop sentinel when done. When a completion condition IS
// configured, buildAutohostContinuePrompt injects it instead (see
// autohostConditionContinueTemplate).
const autohostContinuePrompt = `继续推进当前任务的剩余步骤。遇到选择请按你的最佳判断决定，不要停下来询问我。
保持高效少步：用最少的必要步骤推进，避免重复探索和冗长输出，控制 token 消耗。

仅当整体任务确已彻底完成时，才在回复末尾【单独一行】输出：[AUTOHOST_DONE]；仅完成单个步骤或中间阶段时请继续推进，切勿提前输出该标记。`

// autohostRecoverPrompt is injected after a turn ends with an error AND no
// completion condition is configured. Asks the agent to diagnose the failure
// and try a different approach. The stop sentinel rule still applies so the
// agent can bail out cleanly if recovery is impossible. When a completion
// condition IS configured, buildAutohostRecoverPrompt injects it instead (see
// autohostConditionRecoverTemplate).
const autohostRecoverPrompt = `上一轮以错误结束。请先诊断失败原因（看日志/报错信息），然后换一种方式继续推进。不要重复完全相同的失败操作。
保持高效少步：用最少的必要步骤推进，避免重复探索和冗长输出，控制 token 消耗。

仅当错误确实无法恢复（例如外部依赖不可用、需要人工介入的凭证问题）时，才在回复末尾【单独一行】输出：[AUTOHOST_DONE]；否则请换一种方式继续推进，切勿提前输出该标记。`

// autohostConditionContinueTemplate is the continue prompt used when the
// workspace/issue configures a completion condition (goal_condition). The
// condition is injected verbatim and the agent is asked to self-declare done
// via the [AUTOHOST_DONE] sentinel only once the condition is fully met — this
// is how the (removed) LLM judge's completion signal is re-routed into the
// agent's own turn.
const autohostConditionContinueTemplate = `继续推进当前任务的剩余步骤。遇到选择请按你的最佳判断决定，不要停下来询问我。
保持高效少步：用最少的必要步骤推进，避免重复探索和冗长输出，控制 token 消耗。

本任务的完成条件是：
%s

当且仅当上述完成条件【全部】满足、整体任务确已彻底完成时，才在回复末尾【单独一行】输出：[AUTOHOST_DONE]；仅完成单个步骤或中间阶段时请继续推进，切勿提前输出该标记。`

// autohostConditionRecoverTemplate is the recover prompt used when a completion
// condition is configured. Mirrors autohostConditionContinueTemplate but for the
// error-recovery path: the agent diagnoses the failure, then self-declares done
// once the condition is met OR the error is genuinely unrecoverable.
const autohostConditionRecoverTemplate = `上一轮以错误结束。请先诊断失败原因（看日志/报错信息），然后换一种方式继续推进。不要重复完全相同的失败操作。
保持高效少步：用最少的必要步骤推进，避免重复探索和冗长输出，控制 token 消耗。

本任务的完成条件是：
%s

当且仅当上述完成条件【全部】满足、整体任务确已彻底完成，或错误确实无法恢复（例如外部依赖不可用、需要人工介入的凭证问题）时，才在回复末尾【单独一行】输出：[AUTOHOST_DONE]；否则请继续推进，切勿提前输出该标记。`

// autohostStopSentinel is the exact string the agent emits to signal task
// completion. Matched verbatim (substring) to avoid false positives from
// natural-language phrases like "我完成了 X 之后再做 Y".
const autohostStopSentinel = "[AUTOHOST_DONE]"

// autohostBgWaitMsg is the transient (non-persisted) ping shown when the
// continue-budget is exhausted but background work is still in flight, so the
// watchdog stops continuing and schedules a paced resume to re-check later.
// Leads with ⏩ (info tone in the SPA). Non-persisted: it repeats across a wait.
const autohostBgWaitMsg = "⏩ 后台任务未完成，已安排稍后自动重新检查（工作空间保持运行中）"

// autohostStopPhrases is a defensive secondary heuristic: if the agent's
// final text contains one of these self-declared completion phrases without
// the sentinel, we still stop. Kept short to minimise false positives.
var autohostStopPhrases = []string{
	"任务已完成", "任务完成", "全部完成", "已完成所有",
	"task complete", "task is complete", "all done", "completed all",
}

// autohostContinuationMarkers are forward-looking / progress-report fragments
// that mean the agent is NOT actually done. When the concluding line carries
// one of these, a co-located completion phrase (e.g. "子任务完成，继续下一步",
// or a progress report like "任务完成度 80%") is treated as in-progress
// narration rather than a task-done declaration — the watchdog keeps going.
// This is the fix for "看门狗静默停止": a substring like "任务完成" inside a
// larger word/clause must no longer silently strand an autohost run.
var autohostContinuationMarkers = []string{
	"继续", "下一步", "接下来", "未完成", "还需", "还要", "尚未",
	"完成度", "完成率", "进度",
	"next step", "still working", "to be done", "not done", "in progress",
}

// readAutohostMode returns the workspace's currently-effective permission
// mode (empty string on lookup failure). Reads fresh from DB so the user
// can flip the toggle mid-session.
func (s *WorkspaceSession) readPermissionMode(ctx context.Context) string {
	rows, err := s.q.GetWorkspaceEnv(ctx, store.GetWorkspaceEnvParams{
		WorkspaceID: s.workspaceID,
		Key:         "NIUNIU_PERMISSION_MODE",
	})
	if err != nil || len(rows) == 0 {
		return ""
	}
	return rows[0].Value
}

// readAutohostBudget returns the per-chain auto-continue cap. Honors the
// workspace env NIUNIU_AUTOHOST_BUDGET when set to a non-negative integer;
// falls back to autohostDefaultBudget.
func (s *WorkspaceSession) readAutohostBudget(ctx context.Context) int {
	return s.readAutohostIntEnv(ctx, "NIUNIU_AUTOHOST_BUDGET", autohostDefaultBudget)
}

// readAutohostErrorBudget returns the per-streak error recovery cap.
func (s *WorkspaceSession) readAutohostErrorBudget(ctx context.Context) int {
	return s.readAutohostIntEnv(ctx, "NIUNIU_AUTOHOST_ERROR_BUDGET", autohostDefaultErrorBudget)
}

// readAutohostContinuePrompt returns the message the watchdog injects on
// idle queue. Honors workspace env NIUNIU_AUTOHOST_CONTINUE_PROMPT;
// falls back to autohostContinuePrompt. Empty/whitespace-only values are
// treated as "not set" — a blank prompt would brick the watchdog.
func (s *WorkspaceSession) readAutohostContinuePrompt(ctx context.Context) string {
	return s.readAutohostStringEnv(ctx, "NIUNIU_AUTOHOST_CONTINUE_PROMPT", autohostContinuePrompt)
}

// readAutohostRecoverPrompt returns the message the watchdog injects after
// a turn ends with an error. Honors NIUNIU_AUTOHOST_RECOVER_PROMPT.
func (s *WorkspaceSession) readAutohostRecoverPrompt(ctx context.Context) string {
	return s.readAutohostStringEnv(ctx, "NIUNIU_AUTOHOST_RECOVER_PROMPT", autohostRecoverPrompt)
}

// buildAutohostContinuePrompt routes the user's completion condition into the
// idle-queue continue prompt. When a condition is configured it is injected via
// autohostConditionContinueTemplate (the agent self-declares done with the
// sentinel once met); otherwise the plain continue prompt is used.
func (s *WorkspaceSession) buildAutohostContinuePrompt(ctx context.Context, condition string) string {
	if c := strings.TrimSpace(condition); c != "" {
		return fmt.Sprintf(autohostConditionContinueTemplate, c)
	}
	return s.readAutohostContinuePrompt(ctx)
}

// buildAutohostRecoverPrompt is the error-recovery counterpart of
// buildAutohostContinuePrompt: injects the completion condition via
// autohostConditionRecoverTemplate when configured, else the plain recover
// prompt.
func (s *WorkspaceSession) buildAutohostRecoverPrompt(ctx context.Context, condition string) string {
	if c := strings.TrimSpace(condition); c != "" {
		return fmt.Sprintf(autohostConditionRecoverTemplate, c)
	}
	return s.readAutohostRecoverPrompt(ctx)
}

// readAutohostStringEnv mirrors readAutohostIntEnv for free-text overrides.
// Whitespace-only values are rejected so a stray space doesn't break the
// watchdog. Guards a nil q so test fixtures (and edge-case construction
// paths) can call into this without setting up a full store.
func (s *WorkspaceSession) readAutohostStringEnv(ctx context.Context, key, def string) string {
	if s.q != nil {
		rows, err := s.q.GetWorkspaceEnv(ctx, store.GetWorkspaceEnvParams{
			WorkspaceID: s.workspaceID,
			Key:         key,
		})
		if err == nil && len(rows) > 0 {
			if v := strings.TrimSpace(rows[0].Value); v != "" {
				return v
			}
		}
	}
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// readAutohostIntEnv is the shared lookup: workspace env → process env →
// default. Only non-negative integers are accepted so a typo doesn't disable
// the feature silently.
func (s *WorkspaceSession) readAutohostIntEnv(ctx context.Context, key string, def int) int {
	rows, err := s.q.GetWorkspaceEnv(ctx, store.GetWorkspaceEnvParams{
		WorkspaceID: s.workspaceID,
		Key:         key,
	})
	if err == nil && len(rows) > 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(rows[0].Value)); err == nil && n >= 0 {
			return n
		}
	}
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return def
}

// autohostShouldStop returns true when the agent's last text appears to
// declare the task complete and the watchdog must not auto-continue.
//
// The explicit [AUTOHOST_DONE] sentinel wins anywhere in the text. The
// natural-language fallback is deliberately conservative to avoid silently
// stranding a still-running task on a substring false positive:
//   - it only inspects the concluding (last non-empty) line, so a completion
//     phrase buried in a mid-message recap is ignored;
//   - it skips that line entirely when a continuation / progress-report marker
//     is present (autohostContinuationMarkers);
//   - it ignores subtask-scoped phrasing ("子任务完成" / "分任务完成"), which is
//     a SUBtask finishing, not the whole task.
func autohostShouldStop(lastText string) bool {
	if strings.Contains(lastText, autohostStopSentinel) {
		return true
	}
	line := lastMeaningfulLine(lastText)
	if line == "" {
		return false
	}
	lower := strings.ToLower(line)
	for _, m := range autohostContinuationMarkers {
		if strings.Contains(lower, strings.ToLower(m)) {
			return false
		}
	}
	for _, p := range autohostStopPhrases {
		idx := strings.Index(lower, strings.ToLower(p))
		if idx < 0 {
			continue
		}
		// "子任务完成" / "分任务完成": the char right before the phrase turns it
		// into a SUBtask completion — not the overall task. (本/该/此任务完成 are
		// intentionally NOT excluded: those legitimately mean "this task is done".)
		if r, _ := utf8.DecodeLastRuneInString(line[:idx]); r == '子' || r == '分' {
			continue
		}
		return true
	}
	return false
}

// lastMeaningfulLine returns the last non-empty, whitespace-trimmed line of s,
// or "" when s has no non-empty line. The agent's task-done verdict is its
// concluding statement; judging only this line keeps mid-message progress
// narration from tripping the natural-language stop heuristic.
func lastMeaningfulLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

// autohostMaybeContinue is called by SendLoop after a clean turn ends with an
// empty queue. It returns (shouldContinue, prompt) using a pure sentinel +
// budget decision tree (the LLM completion judge subsystem was removed; the
// user's completion condition is now routed into the injected continue prompt
// instead — see buildAutohostContinuePrompt):
//
//  1. user Stop landed → stop
//  2. permission mode != autohost → stop (watchdog not applicable)
//  3. continue budget <= 0 → stop (auto-continue disabled)
//  4. queue non-empty → yield to the dequeue instead of injecting a continue
//  5. sentinel hit in last text → stop (agent self-declared done)
//  6. consec budget exhausted → schedule a paced bg-wait resume if background
//     work is pending, else stop
//  7. else → increment consec and inject the (condition-aware) continue prompt
func (s *WorkspaceSession) autohostMaybeContinue(ctx context.Context) (bool, string) {
	s.mu.Lock()
	s.autohostScheduledWait = false
	stopReq := s.stopRequested
	s.mu.Unlock()
	// A user Stop landed between turn-end and here: don't continue a session
	// that's being torn down. SendLoop's own stopRequested check exits the loop
	// next.
	if stopReq {
		return false, ""
	}
	if s.readPermissionMode(ctx) != AutohostMode {
		return false, ""
	}
	budget := s.readAutohostBudget(ctx)
	if budget <= 0 {
		return false, ""
	}

	// Queue let-through: autohost must never jump ahead of real queued work.
	// SendLoop dequeues immediately before calling us, so a non-empty queue at
	// this point means an item landed concurrently — most often an
	// error-rollback 'retry' item (server.go SetQueueRollback) re-inserted at
	// the queue front. Decline so SendLoop's follow-up dequeue runs that real
	// item instead of injecting a generic continue prompt that would occupy the
	// running slot while the queued item waits.
	if s.q != nil && s.PendingCount(ctx) > 0 {
		slog.Info("autohost: queue non-empty, yielding to dequeue instead of continue",
			"workspace_id", s.workspaceID)
		return false, ""
	}

	condition, _ := s.readGoalCondition(ctx)
	s.mu.Lock()
	lastText := s.joinedAssistantContent()
	consec := s.autohostConsec
	wasCompact := s.compactTurnActive
	s.compactTurnActive = false
	s.mu.Unlock()

	// The turn that just finished was an auto-injected /compact, not task work.
	// Its assistant text is a conversation SUMMARY which may incidentally contain
	// completion phrases ("任务完成"…) — never let that be read as the task being
	// done. Continue unconditionally (without consuming the continue budget) so
	// the real task resumes on freshly-compacted context.
	if wasCompact {
		slog.Info("autohost: resuming after auto-compact turn", "workspace_id", s.workspaceID)
		return true, s.buildAutohostContinuePrompt(ctx, condition)
	}

	if autohostShouldStop(lastText) {
		slog.Info("autohost: sentinel stop", "workspace_id", s.workspaceID, "consec", consec)
		return false, ""
	}

	if consec >= budget {
		// Budget reached but pending background work (running build / subagent /
		// a wakeup the agent set to re-check) means the task is genuinely not
		// done. Don't abandon it — but don't busy-continue either (that bypasses
		// the budget entirely and can loop forever if the agent re-arms a wakeup
		// every turn). Schedule a PACED resume and stop; finalize keeps the
		// workspace running, and the resume re-checks.
		if s.hasPendingBgWork() && s.scheduleBgWaitResume(ctx) {
			return false, ""
		}
		slog.Info("autohost: budget exhausted, stopping",
			"workspace_id", s.workspaceID, "consec", consec, "budget", budget)
		return false, ""
	}

	s.mu.Lock()
	s.autohostConsec = consec + 1
	s.mu.Unlock()
	slog.Info("autohost: injecting continue",
		"workspace_id", s.workspaceID, "consec", consec+1, "budget", budget)
	return true, s.buildAutohostContinuePrompt(ctx, condition)
}

// autohostReset clears the consecutive-continue counter. Called by SendLoop
// whenever a turn's content comes from outside the watchdog (initial dispatch
// or DB-queue dequeue).
func (s *WorkspaceSession) autohostReset() {
	s.mu.Lock()
	s.autohostConsec = 0
	s.autohostScheduledWait = false
	s.mu.Unlock()
}

// autohostMaybeRecover is called by SendLoop when the most recent turn ended
// with an error. It returns (true, prompt) when the watchdog should retry
// with a recovery prompt; (false, "") to let SendLoop drop into attention.
//
// The watchdog fires when ALL hold:
//   - workspace's NIUNIU_PERMISSION_MODE == autohost
//   - autohostErrorConsec < errorBudget
//   - the agent's last assistant text does NOT contain the stop sentinel
//     (the agent itself can opt out of further retries by emitting it)
//
// The injected prompt is condition-aware (buildAutohostRecoverPrompt): when a
// completion condition is configured the agent is asked to self-declare done
// once it is met (or the error is unrecoverable).
func (s *WorkspaceSession) autohostMaybeRecover(ctx context.Context) (bool, string) {
	if s.readPermissionMode(ctx) != AutohostMode {
		return false, ""
	}
	budget := s.readAutohostErrorBudget(ctx)
	if budget <= 0 {
		return false, ""
	}

	condition, _ := s.readGoalCondition(ctx)
	s.mu.Lock()
	consec := s.autohostErrorConsec
	lastText := s.joinedAssistantContent()
	s.mu.Unlock()

	if consec >= budget {
		slog.Info("autohost: error budget exhausted, stopping",
			"workspace_id", s.workspaceID, "consec", consec, "budget", budget)
		return false, ""
	}
	// Honor the sentinel only — agent self-declaring "done" via natural
	// language during an error turn is unreliable (it's likely the error
	// message itself contains words like "complete" or "完成"). The
	// explicit sentinel stays the agent's escape hatch.
	if strings.Contains(lastText, autohostStopSentinel) {
		slog.Info("autohost: agent emitted stop sentinel during error, stopping",
			"workspace_id", s.workspaceID, "consec", consec)
		return false, ""
	}

	s.mu.Lock()
	s.autohostErrorConsec = consec + 1
	s.mu.Unlock()
	prompt := s.buildAutohostRecoverPrompt(ctx, condition)
	slog.Info("autohost: injecting recovery",
		"workspace_id", s.workspaceID, "consec", consec+1, "budget", budget)
	return true, prompt
}

// autohostResetErrors clears the consecutive-error counter. Called by
// SendLoop after a turn ends without lastTurnError.
func (s *WorkspaceSession) autohostResetErrors() {
	s.mu.Lock()
	s.autohostErrorConsec = 0
	s.mu.Unlock()
}

// readGoalCondition resolves the user's completion condition for the autohost
// continue/recover prompt. Priority: issues.goal_condition (when issueID > 0) →
// workspace_env NIUNIU_AUTOHOST_GOAL_CONDITION → "" (no condition → plain
// continue/recover prompt). source is "issue"/"workspace"/"" and is currently
// informational only.
func (s *WorkspaceSession) readGoalCondition(ctx context.Context) (condition, source string) {
	if s.issueID > 0 && s.q != nil {
		if r, err := s.q.GetIssueGoalCondition(ctx, s.issueID); err == nil {
			if v := strings.TrimSpace(r); v != "" {
				return v, "issue"
			}
		}
	}
	if v := strings.TrimSpace(s.readAutohostStringEnv(ctx, "NIUNIU_AUTOHOST_GOAL_CONDITION", "")); v != "" {
		return v, "workspace"
	}
	// No initial-prompt fallback: if neither the issue nor the workspace
	// explicitly configures a completion condition, the watchdog uses the plain
	// continue/recover prompt (no condition injected).
	return "", ""
}

// truncateRunes caps s to maxRunes runes (not bytes), preventing a multi-byte
// UTF-8 codepoint from being split mid-sequence. Shared utility used by the
// goal-condition / column-op suggestion prompt builders.
func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes])
}
