package agentproxy

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// Auto context compaction: users rarely run /compact themselves, so a long
// chat keeps re-sending the whole history every turn and burns tokens fast.
// This heuristic watches the live context-window occupancy and, once it crosses
// a configurable fraction of the model's context budget, injects a one-shot
// /compact turn so the Claude CLI rewrites its resume history into a summary.
// The next turn then runs on a much smaller prompt.
//
// Why /compact (the real CLI command) and not a "please summarize" prompt: only
// /compact actually shrinks the context the CLI replays on --resume; a natural
// language summary just ADDS tokens. The frontend's manual /compact maps to a
// summarize prompt for display reasons; the automatic path sends the genuine
// command over stdin so the reduction is real.
//
// Codex has no /compact command, so this is Claude-only.

const (
	// autoCompactDefaultBudget is the assumed model context window (tokens) when
	// the workspace does not override it. 1M matches the current large-window
	// default; smaller-window users lower it via the Claude settings dialog.
	autoCompactDefaultBudget = 1000000

	// autoCompactDefaultPercent is the occupancy fraction (percent of budget) at
	// which compaction triggers. The issue's goal is 70%.
	autoCompactDefaultPercent = 70

	// autoCompactCommand is the exact stdin content sent to trigger the CLI's
	// native compaction. It must START with "/compact" for the CLI to parse it as
	// a slash command; the trailing text is the optional focus instruction the
	// command accepts, steering the summary toward what matters for resuming work.
	autoCompactCommand = "/compact 请保留关键决策、改动过的文件路径、未完成的任务状态与重要上下文，简明扼要，省略无关的中间过程。"

	// autoCompactNoticeMsg is the transient (non-persisted) ping shown when an
	// automatic compaction fires, so the user understands the /compact system
	// message was injected by niuniu rather than typed by them. Leads with ♻️.
	autoCompactNoticeMsg = "♻️ 当前对话上下文已达到预算阈值，正在自动压缩以释放 token 空间…"
)

// autoCompactEnabled reports whether the auto-compaction heuristic is on for this
// workspace. Default on; set workspace env NIUNIU_AUTO_COMPACT=0 to disable.
func (s *WorkspaceSession) autoCompactEnabled(ctx context.Context) bool {
	return s.readAutohostIntEnv(ctx, "NIUNIU_AUTO_COMPACT", 1) != 0
}

// autoCompactBudget returns the context-window token budget. Resolution order:
//   1. workspace env NIUNIU_AUTO_COMPACT_BUDGET (explicit override, also read
//      by the process-env fallback inside readAutohostIntEnv)
//   2. the bound provider's context_window, injected by ExpandProvider as the
//      same NIUNIU_AUTO_COMPACT_BUDGET key in the resolved env (sceneenv.Resolve)
//   3. model lookup from s.modelName (provider unset → guess by model family)
//   4. autoCompactDefaultBudget (1M)
//
// The pill (GetClaudeStatus) and the auto-compaction trigger both divide by
// this, so they always measure against the same budget.
func (s *WorkspaceSession) autoCompactBudget(ctx context.Context) int {
	if n := s.readAutohostIntEnv(ctx, "NIUNIU_AUTO_COMPACT_BUDGET", 0); n > 0 {
		return n
	}
	if v := s.resolvedEnvValue(ctx, "NIUNIU_AUTO_COMPACT_BUDGET"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return n
		}
	}
	s.mu.Lock()
	model := s.modelName
	s.mu.Unlock()
	if n := contextWindowSize(model); n > 0 {
		return n
	}
	return autoCompactDefaultBudget
}

// autoCompactPercent returns the trigger threshold as a percent of budget.
// Honors workspace env NIUNIU_AUTO_COMPACT_PERCENT; out-of-range values
// (<=0 or >=100) fall back to the default so a typo can't disable or wedge it.
func (s *WorkspaceSession) autoCompactPercent(ctx context.Context) int {
	p := s.readAutohostIntEnv(ctx, "NIUNIU_AUTO_COMPACT_PERCENT", autoCompactDefaultPercent)
	if p <= 0 || p >= 100 {
		return autoCompactDefaultPercent
	}
	return p
}

// occupancyOverThreshold reports whether the live context occupancy has reached
// `percent`% of `budget`. All-integer math (occupancy*100 >= budget*percent)
// avoids float rounding at the boundary.
func occupancyOverThreshold(occupancy, budget, percent int) bool {
	if budget <= 0 || percent <= 0 || occupancy <= 0 {
		return false
	}
	return occupancy*100 >= budget*percent
}

// shouldAutoCompact is the pure trigger decision: fire when occupancy is over
// the threshold AND a compaction has not already been injected for the current
// high-water episode (suppressed). Kept free of session state for unit testing.
func shouldAutoCompact(occupancy, budget, percent int, suppressed bool) bool {
	if suppressed {
		return false
	}
	return occupancyOverThreshold(occupancy, budget, percent)
}

// maybeAutoCompact decides whether to inject an automatic /compact turn given
// the latest context-window occupancy. It returns (true, command) when SendLoop
// should run /compact as the next (injected, system-rendered) turn.
//
// Re-arm logic lives here: the suppressed flag is cleared as soon as occupancy
// falls back under the threshold (a real compaction did its job, or the context
// otherwise shrank), and set when we inject. If /compact turns out to be a no-op
// in the running CLI, occupancy stays high, the flag stays set, and we simply
// never compact again this session — one wasted turn, never a loop.
func (s *WorkspaceSession) maybeAutoCompact(ctx context.Context) (bool, string) {
	// Codex has no /compact command. Qwen Code (Gemini-CLI lineage) does not use
	// Claude's /compact either, so injecting it would burn a turn on an
	// unrecognized command; its own context management is a PoC follow-up.
	if s.cliType == "codex" || s.cliType == "qwen" || s.cliType == "omp" || s.cliType == "goose" || s.cliType == "cursor" {
		return false, ""
	}
	if !s.autoCompactEnabled(ctx) {
		return false, ""
	}
	budget := s.autoCompactBudget(ctx)
	percent := s.autoCompactPercent(ctx)

	s.mu.Lock()
	occ := s.lastContextTokens
	if s.autoCompactSuppressed && !occupancyOverThreshold(occ, budget, percent) {
		s.autoCompactSuppressed = false
	}
	suppressed := s.autoCompactSuppressed
	fire := shouldAutoCompact(occ, budget, percent, suppressed)
	if fire {
		s.autoCompactSuppressed = true
		s.compactTurnActive = true
	}
	s.mu.Unlock()

	if !fire {
		return false, ""
	}
	slog.Info("auto-compact: context occupancy over threshold, injecting /compact",
		"workspace_id", s.workspaceID, "occupancy", occ, "budget", budget, "percent", percent)
	return true, autoCompactCommand
}

// seedLastContextTokens restores the persisted context occupancy for the
// resumed CLI session (session_state.last_context_tokens). lastContextTokens
// is in-memory only, so without this a long --resume conversation that grew
// across an agent/server restart starts every session at occupancy 0 — the
// boundary check is blind exactly until a turn overflows the model window and
// 400s (the rejected request emits no usage events, so the blind spot never
// heals on its own). Called from ensureProcess once the session id is known;
// a stale seed still errs toward firing late, never spuriously.
func (s *WorkspaceSession) seedLastContextTokens(ctx context.Context) {
	row, err := s.q.GetSessionState(ctx, store.GetSessionStateParams{
		WorkspaceID: s.workspaceID,
		SessionID:   s.sessionId,
	})
	if err != nil || row.LastContextTokens <= 0 {
		return
	}
	s.mu.Lock()
	fresh := s.lastContextTokens == 0
	if fresh {
		s.lastContextTokens = int(row.LastContextTokens)
	}
	s.mu.Unlock()
	if fresh {
		slog.Info("auto-compact: seeded context occupancy from previous run",
			"workspace_id", s.workspaceID, "session", s.sessionId, "tokens", row.LastContextTokens)
	}
}

// onTurnResult runs once per completed turn (result event): it persists the
// live occupancy for the next seed, and re-arms auto-compact when the injected
// /compact turn itself FAILED. Without the re-arm, a failed summary request
// (e.g. the API rejecting the compaction call) leaves occupancy above
// threshold forever — suppressed only re-arms below the threshold — and
// auto-compact goes permanently silent while the context keeps growing.
// A successful turn keeps the flag (the occupancy drop below threshold is the
// real re-arm), preserving the no-loop guarantee.
func (s *WorkspaceSession) onTurnResult(ctx context.Context, isError bool) {
	s.mu.Lock()
	occ := s.lastContextTokens
	compactFailed := s.compactTurnActive && isError
	s.compactTurnActive = false
	if compactFailed {
		s.autoCompactSuppressed = false
	}
	s.mu.Unlock()
	if compactFailed {
		slog.Warn("auto-compact: injected /compact turn failed; re-arming for the next boundary",
			"workspace_id", s.workspaceID)
	}
	if occ > 0 {
		if err := s.q.UpsertSessionLastContextTokens(ctx, store.UpsertSessionLastContextTokensParams{
			WorkspaceID:       s.workspaceID,
			SessionID:         s.sessionId,
			LastContextTokens: int64(occ),
		}); err != nil {
			slog.Warn("auto-compact: persist occupancy failed", "workspace_id", s.workspaceID, "error", err)
		}
	}
}
