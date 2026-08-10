package agentproxy

import (
	"context"
	"strings"
	"testing"
)

// --- pure decision logic -----------------------------------------------------

func TestOccupancyOverThreshold(t *testing.T) {
	cases := []struct {
		name                 string
		occ, budget, percent int
		want                 bool
	}{
		{"well under", 100000, 200000, 70, false},
		{"just under", 139999, 200000, 70, false},
		{"exactly at threshold", 140000, 200000, 70, true},
		{"over", 180000, 200000, 70, true},
		{"zero occupancy", 0, 200000, 70, false},
		{"zero budget disables", 150000, 0, 70, false},
		{"zero percent disables", 150000, 200000, 0, false},
		{"custom 50pct under", 90000, 200000, 50, false},
		{"custom 50pct at", 100000, 200000, 50, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := occupancyOverThreshold(tc.occ, tc.budget, tc.percent); got != tc.want {
				t.Fatalf("occupancyOverThreshold(%d,%d,%d) = %v, want %v",
					tc.occ, tc.budget, tc.percent, got, tc.want)
			}
		})
	}
}

func TestShouldAutoCompact_SuppressedNeverFires(t *testing.T) {
	// Over threshold but suppressed (a compaction already injected) must not fire.
	if shouldAutoCompact(180000, 200000, 70, true) {
		t.Fatal("suppressed episode must not re-trigger")
	}
	// Same occupancy, not suppressed, fires.
	if !shouldAutoCompact(180000, 200000, 70, false) {
		t.Fatal("over-threshold un-suppressed must fire")
	}
}

// --- maybeAutoCompact integration --------------------------------------------

// setContext is a tiny helper to set the live occupancy the way a result event would.
func setContext(s *WorkspaceSession, tokens int) {
	s.mu.Lock()
	s.lastContextTokens = tokens
	s.mu.Unlock()
}

func TestMaybeAutoCompact_FiresAtThreshold(t *testing.T) {
	s, q := newCondTestSession(t)
	ctx := context.Background()

	// Budget 200000, percent 70 -> threshold 140000.
	setWorkspaceEnv(t, q, s.workspaceID, "NIUNIU_AUTO_COMPACT_BUDGET", "200000")
	setContext(s, 120000)
	if ok, _ := s.maybeAutoCompact(ctx); ok {
		t.Fatal("must not fire below threshold")
	}

	setContext(s, 150000) // 75% > 70%
	ok, cmd := s.maybeAutoCompact(ctx)
	if !ok {
		t.Fatal("must fire at/over threshold")
	}
	if !strings.HasPrefix(cmd, "/compact") {
		t.Fatalf("injected command must start with /compact, got %q", cmd)
	}
	// Firing arms the guards.
	s.mu.Lock()
	suppressed, compactActive := s.autoCompactSuppressed, s.compactTurnActive
	s.mu.Unlock()
	if !suppressed || !compactActive {
		t.Fatalf("after firing want suppressed && compactTurnActive, got %v %v", suppressed, compactActive)
	}
}

func TestMaybeAutoCompact_SuppressedUntilReductionThenReArms(t *testing.T) {
	s, q := newCondTestSession(t)
	ctx := context.Background()

	// Budget 200000, percent 70 -> threshold 140000.
	setWorkspaceEnv(t, q, s.workspaceID, "NIUNIU_AUTO_COMPACT_BUDGET", "200000")
	setContext(s, 150000) // 75% -> fire
	if ok, _ := s.maybeAutoCompact(ctx); !ok {
		t.Fatal("first crossing must fire")
	}

	// The /compact turn's own result still reports a large prompt (it read the
	// whole history to summarize). Still over threshold + suppressed -> no re-fire.
	setContext(s, 148000)
	if ok, _ := s.maybeAutoCompact(ctx); ok {
		t.Fatal("must stay suppressed while occupancy remains over threshold")
	}

	// Next real turn runs on the compacted context: occupancy drops by >=30%
	// (150000 -> 100000 = 33% reduction). That re-arms the heuristic.
	setContext(s, 100000) // 50% < 70%
	if ok, _ := s.maybeAutoCompact(ctx); ok {
		t.Fatal("below threshold must not fire")
	}
	s.mu.Lock()
	suppressed := s.autoCompactSuppressed
	s.mu.Unlock()
	if suppressed {
		t.Fatal("dropping below threshold must re-arm (clear suppressed)")
	}

	// Conversation grows past the threshold again -> fires a second time.
	setContext(s, 145000)
	if ok, _ := s.maybeAutoCompact(ctx); !ok {
		t.Fatal("re-armed heuristic must fire on the next crossing")
	}
}

// The goal condition requires post-compaction token reduction >= 30%. This
// asserts the heuristic's contract around that number: a pre-compact occupancy
// over the 70% threshold, and a post-compact occupancy that is >=30% smaller,
// is recognized as a successful compaction (re-armed, ready for the next cycle).
func TestMaybeAutoCompact_ReductionContract(t *testing.T) {
	s, q := newCondTestSession(t)
	ctx := context.Background()

	const budget = 200000
	setWorkspaceEnv(t, q, s.workspaceID, "NIUNIU_AUTO_COMPACT_BUDGET", "200000")
	preCompact := 150000 // 75% of budget -> over the 70% threshold
	postCompact := 100000

	setContext(s, preCompact)
	if ok, _ := s.maybeAutoCompact(ctx); !ok {
		t.Fatalf("pre-compact occupancy %d should trigger at budget %d", preCompact, budget)
	}

	reduction := float64(preCompact-postCompact) / float64(preCompact)
	if reduction < 0.30 {
		t.Fatalf("test fixture invalid: reduction %.0f%% < 30%%", reduction*100)
	}

	// Post-compaction occupancy re-arms (below threshold).
	setContext(s, postCompact)
	s.maybeAutoCompact(ctx)
	s.mu.Lock()
	suppressed := s.autoCompactSuppressed
	s.mu.Unlock()
	if suppressed {
		t.Fatalf(">=30%% reduction (%.0f%%) should re-arm the heuristic", reduction*100)
	}
}

func TestMaybeAutoCompact_CodexSkipped(t *testing.T) {
	s, _ := newCondTestSession(t)
	s.cliType = "codex"
	setContext(s, 190000)
	if ok, _ := s.maybeAutoCompact(context.Background()); ok {
		t.Fatal("codex has no /compact; must never fire")
	}
}

func TestMaybeAutoCompact_DisabledByEnv(t *testing.T) {
	s, q := newCondTestSession(t)
	setWorkspaceEnv(t, q, s.workspaceID, "NIUNIU_AUTO_COMPACT", "0")
	setContext(s, 190000)
	if ok, _ := s.maybeAutoCompact(context.Background()); ok {
		t.Fatal("NIUNIU_AUTO_COMPACT=0 must disable auto-compaction")
	}
}

func TestMaybeAutoCompact_CustomThresholdEnv(t *testing.T) {
	s, q := newCondTestSession(t)
	ctx := context.Background()
	// Lower the budget and threshold so a small context triggers.
	setWorkspaceEnv(t, q, s.workspaceID, "NIUNIU_AUTO_COMPACT_BUDGET", "10000")
	setWorkspaceEnv(t, q, s.workspaceID, "NIUNIU_AUTO_COMPACT_PERCENT", "50")

	setContext(s, 4000) // 40% < 50%
	if ok, _ := s.maybeAutoCompact(ctx); ok {
		t.Fatal("must not fire below custom threshold")
	}
	setContext(s, 6000) // 60% >= 50%
	if ok, _ := s.maybeAutoCompact(ctx); !ok {
		t.Fatal("must fire over custom threshold")
	}
}

func TestAutoCompactPercent_OutOfRangeFallsBack(t *testing.T) {
	s, q := newCondTestSession(t)
	ctx := context.Background()
	for _, v := range []string{"0", "100", "150"} {
		setWorkspaceEnv(t, q, s.workspaceID, "NIUNIU_AUTO_COMPACT_PERCENT", v)
		if got := s.autoCompactPercent(ctx); got != autoCompactDefaultPercent {
			t.Fatalf("percent=%q should fall back to %d, got %d", v, autoCompactDefaultPercent, got)
		}
	}
}

// --- integration: real message_start event drives the trigger end-to-end -----

// messageStartEvent builds a Claude "message_start" stream ParsedEvent carrying
// ONE API request's usage — the per-request prompt size the heuristic reads as
// live context occupancy, exactly as adapter/claude.go produces it.
func messageStartEvent(input, cacheRead, cacheCreate, output int) ParsedEvent {
	return ParsedEvent{
		Type:                "stream_event",
		StreamEventType:     "message_start",
		InputTokens:         input,
		CacheReadTokens:     cacheRead,
		CacheCreationTokens: cacheCreate,
		OutputTokens:        output,
	}
}

// resultEvent builds a non-error Claude "result" ParsedEvent. Its usage is the
// turn-CUMULATIVE total (sums every tool round-trip), so it must NOT move the
// occupancy gauge — only feed cost/token accounting.
func resultEvent(input, cacheRead, cacheCreate, output int) ParsedEvent {
	return ParsedEvent{
		Type:                "result",
		Result:              "ok",
		InputTokens:         input,
		CacheReadTokens:     cacheRead,
		CacheCreationTokens: cacheCreate,
		OutputTokens:        output,
	}
}

// TestHandleEvent_MessageStartDrivesAutoCompaction is the integration test: it
// pushes real message_start stream events through the actual event handler, then
// asserts the heuristic reads occupancy correctly. The wire the goal cares about:
//   - occupancy = one request's input + cache_read + cache_creation (message_start)
//   - the turn-cumulative result event must NOT overwrite the live occupancy
//     (regression: a single multi-tool question used to trip compaction)
//   - trigger fires once occupancy >= 70% of the budget
//   - a post-compaction prompt with >=30% fewer tokens re-arms the heuristic
func TestHandleEvent_MessageStartDrivesAutoCompaction(t *testing.T) {
	s, q := newCondTestSession(t)
	s.hub = NewSessionHub()
	t.Cleanup(s.hub.Stop)
	s.cliType = "claude"
	ctx := context.Background()

	// Budget 10000, default 70% -> threshold 7000.
	setWorkspaceEnv(t, q, s.workspaceID, "NIUNIU_AUTO_COMPACT_BUDGET", "10000")

	// One request's prompt: occupancy = 3000 + 4000 + 500 = 7500 (75% > 70%).
	s.handleEvent(ctx, messageStartEvent(3000, 4000, 500, 1), "msg-1")
	s.mu.Lock()
	occ := s.lastContextTokens
	s.mu.Unlock()
	if occ != 7500 {
		t.Fatalf("occupancy must sum input+cache_read+cache_creation = 7500, got %d", occ)
	}

	// Regression: the turn's result event carries a huge cumulative cache_read
	// (every tool round-trip summed) and must NOT overwrite the live occupancy.
	s.handleEvent(ctx, resultEvent(9000, 900000, 5000, 4000), "msg-1")
	s.mu.Lock()
	occAfterResult := s.lastContextTokens
	s.mu.Unlock()
	if occAfterResult != 7500 {
		t.Fatalf("turn-cumulative result must not drive occupancy: got %d, want 7500", occAfterResult)
	}

	ok, cmd := s.maybeAutoCompact(ctx)
	if !ok {
		t.Fatal("75%% occupancy must trigger auto-compaction at the 70%% threshold")
	}
	if !strings.HasPrefix(cmd, "/compact") {
		t.Fatalf("must inject a /compact command, got %q", cmd)
	}

	// Post-compaction the next request's prompt shrinks: occupancy drops to
	// 2000 + 2000 + 0 = 4000, a (7500-4000)/7500 = 46.7% reduction (>=30%).
	s.handleEvent(ctx, messageStartEvent(2000, 2000, 0, 1), "msg-2")
	s.mu.Lock()
	occ2 := s.lastContextTokens
	s.mu.Unlock()
	if occ2 != 4000 {
		t.Fatalf("post-compaction occupancy = %d, want 4000", occ2)
	}
	reduction := float64(occ-occ2) / float64(occ)
	if reduction < 0.30 {
		t.Fatalf("reduction %.1f%% < 30%% — fixture invalid", reduction*100)
	}

	// The >=30% reduction (now under threshold) must re-arm, not re-fire.
	if ok, _ := s.maybeAutoCompact(ctx); ok {
		t.Fatal("post-compaction occupancy is under threshold; must not re-fire")
	}
	s.mu.Lock()
	suppressed := s.autoCompactSuppressed
	s.mu.Unlock()
	if suppressed {
		t.Fatal(">=30%% reduction below threshold must clear the suppressed flag (re-arm)")
	}
}

// --- autohost guard: a compaction summary must not be read as task completion -

func TestAutohostMaybeContinue_CompactTurnForcesContinue(t *testing.T) {
	s, q := newCondTestSession(t)
	ctx := context.Background()
	// Enable autohost so autohostMaybeContinue is active.
	setWorkspaceEnv(t, q, s.workspaceID, "NIUNIU_PERMISSION_MODE", AutohostMode)

	// The just-finished turn was an auto /compact whose summary text contains a
	// completion phrase that would normally stop autohost.
	s.mu.Lock()
	s.compactTurnActive = true
	s.assistantTextBlocks = []string{"对话已压缩。任务完成的部分包括 …"}
	s.mu.Unlock()

	ok, prompt := s.autohostMaybeContinue(ctx)
	if !ok {
		t.Fatal("after a compact turn autohost must continue, not stop on the summary text")
	}
	if prompt == "" {
		t.Fatal("continue prompt must be non-empty")
	}
	s.mu.Lock()
	stillActive := s.compactTurnActive
	s.mu.Unlock()
	if stillActive {
		t.Fatal("compactTurnActive must be consumed (cleared) after the guard fires")
	}
}

// --- /claude-status: displayed window % must match the compaction occupancy ---

// TestGetClaudeStatus_UsesFullOccupancy pins that the window percentage shown in
// the chat-input pill (the heartbeat icon) reflects the live occupancy
// (input + cache_read + cache_creation) — the SAME signal that drives
// auto-compaction — not the uncached input_tokens alone. Before the fix it
// divided input_tokens by the window, so a cached conversation (whose replayed
// history lives in cache_read) read a few percent in the pill while compaction
// fired at 70%; the two disagreed visibly.
func TestGetClaudeStatus_UsesFullOccupancy(t *testing.T) {
	s, _ := newCondTestSession(t)
	ctx := context.Background()

	// Fresh session: no occupancy yet -> 0% used, only the window size reported.
	st := s.GetClaudeStatus(ctx)
	if st.ContextWindow == nil || st.ContextWindow.UsedPercentage != 0 {
		t.Fatalf("fresh session must report 0%% used, got %+v", st.ContextWindow)
	}

	// A heavily cached request: tiny uncached input, huge cache_read.
	const input, cacheRead, cacheCreate = 10000, 600000, 40000
	occupancy := input + cacheRead + cacheCreate // 650000
	setContext(s, occupancy)

	st = s.GetClaudeStatus(ctx)
	if st.ContextWindow == nil {
		t.Fatal("context_window must be present once occupancy is known")
	}
	// The denominator is the workspace-configured budget (autoCompactBudget) — the
	// same value auto-compaction divides by — NOT contextWindowSize(model).
	maxTokens := s.autoCompactBudget(ctx)
	wantPct := float64(occupancy) / float64(maxTokens) * 100
	if diff := st.ContextWindow.UsedPercentage - wantPct; diff > 0.01 || diff < -0.01 {
		t.Fatalf("used_percentage = %.4f, want %.4f (full occupancy)", st.ContextWindow.UsedPercentage, wantPct)
	}
	if st.ContextWindow.MaxTokens != maxTokens {
		t.Fatalf("max_tokens = %d, want configured budget %d", st.ContextWindow.MaxTokens, maxTokens)
	}
	// Guard against regressing to input-only, which would read ~65x lower here.
	inputOnlyPct := float64(input) / float64(maxTokens) * 100
	if st.ContextWindow.UsedPercentage <= inputOnlyPct*2 {
		t.Fatalf("percentage %.2f looks input-only (input-only=%.2f); it must include cache tokens",
			st.ContextWindow.UsedPercentage, inputOnlyPct)
	}
	if st.ContextWindow.InputTokens != occupancy {
		t.Fatalf("reported tokens = %d, want full occupancy %d", st.ContextWindow.InputTokens, occupancy)
	}
}

// TestGetClaudeStatus_DenominatorTracksConfiguredBudget pins that the displayed
// window denominator follows the workspace's configured budget. A user who sets
// NIUNIU_AUTO_COMPACT_BUDGET=200000 sees the pill measure occupancy against
// 200k — the same budget auto-compaction triggers on — so the pill reads the
// trigger percent exactly when compaction fires, instead of against the model's
// physical 1M window.
func TestGetClaudeStatus_DenominatorTracksConfiguredBudget(t *testing.T) {
	s, q := newCondTestSession(t)
	ctx := context.Background()

	setWorkspaceEnv(t, q, s.workspaceID, "NIUNIU_AUTO_COMPACT_BUDGET", "200000")

	const occupancy = 140000 // 70% of the configured 200k budget
	setContext(s, occupancy)

	st := s.GetClaudeStatus(ctx)
	if st.ContextWindow == nil {
		t.Fatal("context_window must be present once occupancy is known")
	}
	if st.ContextWindow.MaxTokens != 200000 {
		t.Fatalf("max_tokens = %d, want configured 200000", st.ContextWindow.MaxTokens)
	}
	if diff := st.ContextWindow.UsedPercentage - 70.0; diff > 0.01 || diff < -0.01 {
		t.Fatalf("used_percentage = %.4f, want 70.0 against the 200k budget", st.ContextWindow.UsedPercentage)
	}
}
