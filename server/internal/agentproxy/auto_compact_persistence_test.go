package agentproxy

import (
	"context"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// TestMaybeAutoCompact_SeededOccupancyFiresAfterRestart covers the restart
// amnesia fix: a long --resume conversation grows across agent/server
// restarts, and lastContextTokens is in-memory only. After a restart the
// FIRST turn replays the whole history, so the seed from session_state must
// make the very first boundary check see the real occupancy and fire.
func TestMaybeAutoCompact_SeededOccupancyFiresAfterRestart(t *testing.T) {
	ctx := context.Background()
	s := newDispatchTestSession(t)
	s.sessionId = "sess-restart-test"
	setWorkspaceEnv(t, s.q, s.workspaceID, "NIUNIU_AUTO_COMPACT_BUDGET", "1000000")

	if err := s.q.UpsertSessionLastContextTokens(ctx, store.UpsertSessionLastContextTokensParams{
		WorkspaceID:       s.workspaceID,
		SessionID:         s.sessionId,
		LastContextTokens: 900000,
	}); err != nil {
		t.Fatalf("UpsertSessionLastContextTokens: %v", err)
	}

	// Fresh process: in-memory occupancy is 0 until the seed runs.
	s.mu.Lock()
	if s.lastContextTokens != 0 {
		t.Fatalf("precondition: fresh session occupancy = %d, want 0", s.lastContextTokens)
	}
	s.mu.Unlock()

	s.seedLastContextTokens(ctx)

	ok, cmd := s.maybeAutoCompact(ctx)
	if !ok {
		t.Fatal("expected auto-compact to fire on the first boundary after restart (seeded occupancy 900k >= 70% of 1M)")
	}
	if !strings.HasPrefix(cmd, "/compact") {
		t.Errorf("expected /compact command, got %q", cmd)
	}
}

// TestOnTurnResult_ErrorReArmsCompact covers the suppress wedge: once an
// auto /compact has been injected, a FAILED compact turn (e.g. the summary
// request itself 400s) must re-arm the heuristic — otherwise occupancy stays
// above threshold forever and auto-compact never fires again while the
// context grows into a hard API rejection.
func TestOnTurnResult_ErrorReArmsCompact(t *testing.T) {
	ctx := context.Background()
	s := newDispatchTestSession(t)
	s.sessionId = "sess-wedge-test"
	setWorkspaceEnv(t, s.q, s.workspaceID, "NIUNIU_AUTO_COMPACT_BUDGET", "1000000")

	s.mu.Lock()
	s.lastContextTokens = 900000
	s.autoCompactSuppressed = true // a compact was already injected once
	s.compactTurnActive = true     // ...and that injected turn is what just finished
	s.mu.Unlock()

	s.onTurnResult(ctx, true) // the injected compact turn errored

	if ok, _ := s.maybeAutoCompact(ctx); !ok {
		t.Fatal("expected auto-compact to re-arm after the compact turn errored")
	}
}

// TestOnTurnResult_SuccessStaysSuppressed locks the no-loop guarantee: a
// SUCCESSFUL turn result must not clear the suppressed flag (occupancy
// dropping below threshold is what re-arms it), so a no-op /compact can
// never loop.
func TestOnTurnResult_SuccessStaysSuppressed(t *testing.T) {
	ctx := context.Background()
	s := newDispatchTestSession(t)
	s.sessionId = "sess-noop-test"
	setWorkspaceEnv(t, s.q, s.workspaceID, "NIUNIU_AUTO_COMPACT_BUDGET", "1000000")

	s.mu.Lock()
	s.lastContextTokens = 900000
	s.autoCompactSuppressed = true
	s.compactTurnActive = true
	s.mu.Unlock()

	s.onTurnResult(ctx, false)

	if ok, _ := s.maybeAutoCompact(ctx); ok {
		t.Fatal("expected suppressed flag to survive a successful compact turn (no-loop guarantee)")
	}
}

// TestOnTurnResult_PersistsOccupancy verifies the write side of the seed:
// a completed turn persists the live occupancy into session_state, keyed by
// the workspace and the CLI session id.
func TestOnTurnResult_PersistsOccupancy(t *testing.T) {
	ctx := context.Background()
	s := newDispatchTestSession(t)
	s.sessionId = "sess-persist-test"

	s.mu.Lock()
	s.lastContextTokens = 654321
	s.mu.Unlock()

	s.onTurnResult(ctx, false)

	row, err := s.q.GetSessionState(ctx, store.GetSessionStateParams{
		WorkspaceID: s.workspaceID,
		SessionID:   s.sessionId,
	})
	if err != nil {
		t.Fatalf("GetSessionState: %v", err)
	}
	if row.LastContextTokens != 654321 {
		t.Errorf("persisted occupancy = %d, want 654321", row.LastContextTokens)
	}
}
