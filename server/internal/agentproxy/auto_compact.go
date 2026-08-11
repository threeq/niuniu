package agentproxy

import (
	"context"
	"log/slog"
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

// autoCompactBudget returns the context-window token budget. Honors workspace
// env NIUNIU_AUTO_COMPACT_BUDGET; falls back to autoCompactDefaultBudget.
func (s *WorkspaceSession) autoCompactBudget(ctx context.Context) int {
	return s.readAutohostIntEnv(ctx, "NIUNIU_AUTO_COMPACT_BUDGET", autoCompactDefaultBudget)
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
	if s.cliType == "codex" || s.cliType == "qwen" || s.cliType == "omp" || s.cliType == "goose" {
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
