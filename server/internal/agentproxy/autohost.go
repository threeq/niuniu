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

	"github.com/niuniu-dev/niuniu/internal/completion"
	"github.com/niuniu-dev/niuniu/internal/sceneenv"
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
const autohostDefaultBudget = 36

// autohostDefaultErrorBudget bounds consecutive auto-recovery attempts per
// error streak. Tunable via NIUNIU_AUTOHOST_ERROR_BUDGET. Set 0 to disable
// error recovery — agent will stop on the first error.
const autohostDefaultErrorBudget = 9

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

// autohostHumanActionSentinel is the marker a worker emits when it needs a
// human design/tradeoff decision it must not make on its own (design §6.5
// "Stop by impact"). The rest of that line is the question surfaced to the
// user; the watchdog then stops the loop into attention instead of spinning.
const autohostHumanActionSentinel = "[AUTOHOST_HUMAN_ACTION]"

// autohostAcceptanceKind is the issue_checklists.kind value that materializes a
// Task Brief acceptance item (design §6.4). Only these rows gate completion —
// manual kind='task' progress rows never block it.
const autohostAcceptanceKind = "acceptance"

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
		return s.augmentAutohostPrompt(ctx, fmt.Sprintf(autohostConditionContinueTemplate, c))
	}
	return s.augmentAutohostPrompt(ctx, s.readAutohostContinuePrompt(ctx))
}

// buildAutohostRecoverPrompt is the error-recovery counterpart of
// buildAutohostContinuePrompt: injects the completion condition via
// autohostConditionRecoverTemplate when configured, else the plain recover
// prompt.
func (s *WorkspaceSession) buildAutohostRecoverPrompt(ctx context.Context, condition string) string {
	if c := strings.TrimSpace(condition); c != "" {
		return s.augmentAutohostPrompt(ctx, fmt.Sprintf(autohostConditionRecoverTemplate, c))
	}
	return s.augmentAutohostPrompt(ctx, s.readAutohostRecoverPrompt(ctx))
}

// augmentAutohostPrompt appends the two policy-driven instructions to a
// continue/recover prompt:
//   - the outstanding Task Brief acceptance items (each must be marked complete
//     via update_checklist_item once done — the watchdog validates them before
//     accepting [AUTOHOST_DONE]);
//   - the [AUTOHOST_HUMAN_ACTION] escape hatch when StopOnDecision is on.
//
// It is a no-op for sessions not bound to an issue (standalone workspaces), so
// the legacy plain/condition prompts are byte-identical there.
func (s *WorkspaceSession) augmentAutohostPrompt(ctx context.Context, base string) string {
	if s.issueID <= 0 || s.q == nil {
		return base
	}
	policy, ok, err := s.autohostEffectivePolicy(ctx)
	if err != nil || !ok {
		// The watchdog fails closed on a policy error before this prompt is
		// used; return the base prompt rather than guessing at policy values.
		return base
	}
	var b strings.Builder
	b.WriteString(base)
	if unmet, aerr := s.unmetAcceptance(ctx); aerr == nil && len(unmet) > 0 {
		b.WriteString("\n\n以下验收项尚未标记完成；完成每一项后请调用 update_checklist_item 将其勾选（completed=true）：\n")
		for _, item := range unmet {
			fmt.Fprintf(&b, "- %s\n", item)
		}
	}
	if policy.StopOnDecision {
		b.WriteString("\n如果你遇到必须由我（人类）裁决的设计或取舍决策，请停止推进，并在回复末尾【单独一行】输出：\n")
		b.WriteString(autohostHumanActionSentinel + " <需要我决定的具体问题>\n")
	}
	return b.String()
}

// autohostEffectivePolicy resolves the issue's effective completion policy
// (design §6.5). It reads fresh rows on every call, which is exactly what makes
// a settings/profile change take effect on the NEXT turn without restarting the
// agent.
//
// resolved=false with a nil error means the session is deliberately not
// issue-bound; callers then keep the legacy NIUNIU_AUTOHOST_* env behavior. For
// an issue-bound session a resolution error is returned to the caller, which
// MUST fail closed (stop the watchdog) rather than silently degrade to the
// permissive env defaults — a DB hiccup must never let a task be declared done.
func (s *WorkspaceSession) autohostEffectivePolicy(ctx context.Context) (completion.EffectivePolicy, bool, error) {
	if s.issueID <= 0 || s.q == nil {
		return completion.EffectivePolicy{}, false, nil
	}
	policy, err := completion.NewResolver(s.q).ResolveIssueID(ctx, s.issueID)
	if err != nil {
		return completion.EffectivePolicy{}, false, err
	}
	return policy, true, nil
}

// unmetAcceptance returns the Task Brief acceptance items not yet completed for
// the current issue. No brief (zero acceptance rows) yields nil — a hand-created
// issue without a Brief keeps the pre-Brief completion behavior. Manual
// kind='task' checklist rows are deliberately excluded (design §6.4).
//
// A query error is returned (not swallowed): the caller must fail closed so a
// transient DB failure can never be read as "all acceptance items are done".
func (s *WorkspaceSession) unmetAcceptance(ctx context.Context) ([]string, error) {
	if s.issueID <= 0 || s.q == nil {
		return nil, nil
	}
	rows, err := s.q.ListChecklistsByIssueAndKind(ctx, store.ListChecklistsByIssueAndKindParams{
		IssueID: s.issueID,
		Kind:    autohostAcceptanceKind,
	})
	if err != nil {
		return nil, err
	}
	var unmet []string
	for _, r := range rows {
		if r.IsCompleted == 0 {
			unmet = append(unmet, r.Title)
		}
	}
	return unmet, nil
}

// autohostHumanActionQuestion extracts the question following the
// [AUTOHOST_HUMAN_ACTION] marker. ok reports whether the worker asked for a
// human decision at all; an empty question still counts (generic wording is
// supplied by the caller).
func autohostHumanActionQuestion(text string) (string, bool) {
	idx := strings.Index(text, autohostHumanActionSentinel)
	if idx < 0 {
		return "", false
	}
	rest := strings.TrimSpace(text[idx+len(autohostHumanActionSentinel):])
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = strings.TrimSpace(rest[:nl])
	}
	return rest, true
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
	if s.q != nil {
		rows, err := s.q.GetWorkspaceEnv(ctx, store.GetWorkspaceEnvParams{
			WorkspaceID: s.workspaceID,
			Key:         key,
		})
		if err == nil && len(rows) > 0 {
			if n, err := strconv.Atoi(strings.TrimSpace(rows[0].Value)); err == nil && n >= 0 {
				return n
			}
		}
	}
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return def
}

// resolvedEnvValue looks up one key from the FULL resolved env (bound provider
// + scene + workspace_env, see sceneenv.Resolve). Used for control keys a
// provider expansion injects — they never exist as raw workspace_env rows.
// Returns "" when absent.
func (s *WorkspaceSession) resolvedEnvValue(ctx context.Context, key string) string {
	if s.q == nil {
		return ""
	}
	envVars, err := sceneenv.Resolve(ctx, s.q, s.workspaceID)
	if err != nil {
		return ""
	}
	for _, e := range envVars {
		if e.Key == key {
			return e.Value
		}
	}
	return ""
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
// empty queue. It returns (shouldContinue, prompt):
//
//  1. user Stop landed → stop
//  2. permission mode != autohost → stop (watchdog not applicable)
//  3. effective completion policy (issue-bound) disables autohost → stop
//  4. continue budget <= 0 → stop (auto-continue disabled)
//  5. queue non-empty → yield to the dequeue instead of injecting a continue
//  6. worker asked for a human decision ([AUTOHOST_HUMAN_ACTION]) → attention
//  7. agent self-declared done ([AUTOHOST_DONE]) → validate Task Brief
//     acceptance: all met → stop (clean completion); unmet → keep going, or at
//     the cap stop into attention with one structured question
//  8. token budget reached → attention with one structured question
//  9. consec budget exhausted → schedule a paced bg-wait resume if background
//     work is pending, else attention with one structured question
//  10. else → increment consec and inject the (condition-aware) continue prompt
//
// The effective policy is resolved from the DB on every call, so a settings or
// profile change takes effect on the NEXT turn without restarting the agent.
func (s *WorkspaceSession) autohostMaybeContinue(ctx context.Context) (bool, string) {
	s.mu.Lock()
	s.autohostScheduledWait = false
	s.autohostAttention = ""
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

	policy, hasPolicy, perr := s.autohostEffectivePolicy(ctx)
	if perr != nil {
		// Fail closed: an issue-bound session that cannot read its policy must
		// not fall back to the permissive legacy budgets. Park into attention so
		// a human checks the configuration instead of the task silently ending.
		slog.Error("autohost: resolve completion policy failed; stopping into attention",
			"workspace_id", s.workspaceID, "issue_id", s.issueID, "error", perr)
		s.setAutohostAttention(autohostPolicyUnavailableQuestion())
		return false, ""
	}
	budget := s.readAutohostBudget(ctx)
	var tokenBudget int64
	if hasPolicy {
		if !policy.AutohostEnabled {
			slog.Info("autohost: disabled by completion policy",
				"workspace_id", s.workspaceID, "issue_id", s.issueID)
			return false, ""
		}
		budget = int(policy.MaxRounds)
		tokenBudget = policy.TokenBudget
	}
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
	chainTokens := s.autohostChainTokens
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

	// Stop by impact: the worker flagged a decision only a human may make.
	if question, ok := autohostHumanActionQuestion(lastText); ok {
		s.setAutohostAttention(autohostDecisionQuestion(question))
		slog.Info("autohost: worker requested a human decision, stopping into attention",
			"workspace_id", s.workspaceID, "consec", consec)
		return false, ""
	}

	if autohostShouldStop(lastText) {
		unmet, aerr := s.unmetAcceptance(ctx)
		if aerr != nil {
			// Fail closed: never accept a sentinel when the acceptance list
			// could not be read — "unknown" must not mean "all done".
			slog.Error("autohost: acceptance lookup failed; refusing completion",
				"workspace_id", s.workspaceID, "issue_id", s.issueID, "error", aerr)
			s.setAutohostAttention(autohostAcceptanceUnavailableQuestion())
			return false, ""
		}
		if len(unmet) == 0 {
			slog.Info("autohost: sentinel accepted, acceptance complete",
				"workspace_id", s.workspaceID, "consec", consec)
			return false, ""
		}
		// The model says done but the Task Brief's acceptance items are not all
		// ticked. Do not announce completion; continue the work. At the cap we
		// must hand over to a human with a single structured question.
		slog.Info("autohost: sentinel rejected, acceptance incomplete",
			"workspace_id", s.workspaceID, "consec", consec, "unmet", len(unmet))
		if tokenBudget > 0 && chainTokens >= tokenBudget {
			s.setAutohostAttention(autohostTokenBudgetQuestion(chainTokens, tokenBudget, unmet))
			return false, ""
		}
		if consec >= budget {
			s.setAutohostAttention(autohostAcceptanceCapQuestion(budget, unmet))
			return false, ""
		}
		s.mu.Lock()
		s.autohostConsec = consec + 1
		s.mu.Unlock()
		return true, s.buildAutohostContinuePrompt(ctx, condition)
	}

	if tokenBudget > 0 && chainTokens >= tokenBudget {
		s.setAutohostAttention(autohostTokenBudgetQuestion(chainTokens, tokenBudget, s.acceptanceListForQuestion(ctx)))
		slog.Info("autohost: token budget exhausted, stopping into attention",
			"workspace_id", s.workspaceID, "tokens", chainTokens, "budget", tokenBudget)
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
		s.setAutohostAttention(autohostRoundsCapQuestion(budget, s.acceptanceListForQuestion(ctx)))
		slog.Info("autohost: round budget exhausted, stopping into attention",
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

// setAutohostAttention records why the watchdog deliberately stopped into
// attention. SendLoop peeks it to finalize with the attention status and to
// surface the structured question, without taking the error/rollback path.
func (s *WorkspaceSession) setAutohostAttention(reason string) {
	s.mu.Lock()
	s.autohostAttention = reason
	s.mu.Unlock()
}

// peekAutohostAttention returns the pending attention reason without clearing
// it; finalizeSendLoopTurn owns the clear so a mid-loop read stays safe.
func (s *WorkspaceSession) peekAutohostAttention() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.autohostAttention
}

// acceptanceListForQuestion is the display-only acceptance list embedded in a
// structured question. A lookup failure is surfaced inline — the question is
// still asked, and the failure is never treated as "all done".
func (s *WorkspaceSession) acceptanceListForQuestion(ctx context.Context) []string {
	unmet, err := s.unmetAcceptance(ctx)
	if err != nil {
		slog.Warn("autohost: acceptance lookup failed while building question",
			"workspace_id", s.workspaceID, "issue_id", s.issueID, "error", err)
		return []string{"（验收项读取失败，请人工核对）"}
	}
	return unmet
}

func autohostPolicyUnavailableQuestion() string {
	return "⏸ 自动托管已暂停：无法解析本任务的自动完成策略（可能是配置或数据库异常）。\n" +
		"请检查项目「自动完成」设置或 issue 指定的 profile 后重试。"
}

func autohostAcceptanceUnavailableQuestion() string {
	return "⏸ 自动托管已暂停：无法读取 Task Brief 的验收项，因此不会宣布完成。\n" +
		"请检查数据库与任务配置，确认验收项后决定是否继续。"
}

func autohostDecisionQuestion(question string) string {
	if strings.TrimSpace(question) == "" {
		question = "当前任务需要人工裁决一个设计/取舍问题（worker 未提供具体问题）。"
	}
	return "⏸ 自动托管已暂停：需要你做一个决策后才能继续。\n问题：" + question +
		"\n请直接回复你的决定；我会带着它继续推进。"
}

func autohostRoundsCapQuestion(budget int, unmet []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "⏸ 自动托管已暂停：已达到最大轮数（%d 轮），任务仍未完成。", budget)
	writeUnmetAcceptance(&b, unmet)
	b.WriteString("\n请回答：是增加轮数预算继续推进，还是调整任务范围/验收标准后重试？")
	return b.String()
}

func autohostTokenBudgetQuestion(used, budget int64, unmet []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "⏸ 自动托管已暂停：已达到 token 预算（%d / %d），上下文已保留。", used, budget)
	writeUnmetAcceptance(&b, unmet)
	b.WriteString("\n请回答：是调高 token 预算继续，还是收缩任务范围后重试？")
	return b.String()
}

func autohostAcceptanceCapQuestion(budget int, unmet []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "⏸ 自动托管已暂停：已达到最大轮数（%d 轮），Task Brief 验收项仍未全部完成。", budget)
	writeUnmetAcceptance(&b, unmet)
	b.WriteString("\n请回答：是放宽/调整验收标准，还是继续投入更多轮次完成剩余验收项？")
	return b.String()
}

func writeUnmetAcceptance(b *strings.Builder, unmet []string) {
	if len(unmet) == 0 {
		return
	}
	b.WriteString("\n未完成的验收项：")
	for _, item := range unmet {
		fmt.Fprintf(b, "\n- %s", item)
	}
}

// autohostReset clears the consecutive-continue counter. Called by SendLoop
// whenever a turn's content comes from outside the watchdog (initial dispatch
// or DB-queue dequeue).
func (s *WorkspaceSession) autohostReset() {
	s.mu.Lock()
	s.autohostConsec = 0
	s.autohostScheduledWait = false
	s.autohostChainTokens = 0
	s.autohostAttention = ""
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
	// Recovery is part of the Completion capability: a policy with autohost
	// switched off must stop retrying too (resolved fresh each turn).
	policy, hasPolicy, perr := s.autohostEffectivePolicy(ctx)
	if perr != nil {
		// Fail closed on a policy read failure (see autohostMaybeContinue).
		slog.Error("autohost: resolve completion policy failed during recovery; stopping",
			"workspace_id", s.workspaceID, "issue_id", s.issueID, "error", perr)
		return false, ""
	}
	if hasPolicy && !policy.AutohostEnabled {
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
	chainTokens := s.autohostChainTokens
	s.mu.Unlock()

	// Token budget bounds recovery too — an error loop must not burn past it.
	if hasPolicy && policy.TokenBudget > 0 && chainTokens >= policy.TokenBudget {
		slog.Info("autohost: token budget exhausted during recovery, stopping",
			"workspace_id", s.workspaceID, "tokens", chainTokens, "budget", policy.TokenBudget)
		return false, ""
	}
	if _, ok := autohostHumanActionQuestion(lastText); ok {
		slog.Info("autohost: worker requested a human decision during an error turn, stopping",
			"workspace_id", s.workspaceID, "consec", consec)
		return false, ""
	}

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
