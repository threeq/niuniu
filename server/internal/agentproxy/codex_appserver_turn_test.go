package agentproxy

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/agentproxy/adapter"
	"github.com/niuniu-dev/niuniu/internal/config"
)

// TestSendAllocatesTurnDoneForCodexTurn is the regression guard for the
// "codex 工作区永远卡在执行中" incident: 7beb223 moved the per-turn
// `s.turnDone = make(...)` allocation below the engine dispatch (to keep a
// replaced process's stale exit signal off the new turn), which left only the
// long-running claude branch with a completion channel. The codex app-server
// runner is the one engine that blocks in waitForTurnComplete — with a nil
// turnDone its turn/completed result event could never unblock it, so the
// workspace stayed "running" after every reply until the 15-minute inactivity
// watchdog killed the app-server and failed the turn. Send must allocate a
// fresh turnDone for EVERY engine, before dispatch.
func TestSendAllocatesTurnDoneForCodexTurn(t *testing.T) {
	s := newDispatchTestSession(t)
	s.cliAdapter = adapter.CodexAdapter{}
	s.cfg = &config.Config{}

	// A missing workDir makes ensureCodexAppServer fail fast (no codex binary
	// needed): the turn errors through the normal result path, Send returns,
	// and the per-turn channel must already exist.
	workDir := filepath.Join(t.TempDir(), "missing")
	if err := s.Send(context.Background(), workDir, "hi", "", false); err == nil {
		t.Fatal("expected Send to fail on a missing workDir; got nil")
	}

	s.mu.Lock()
	ch := s.turnDone
	s.mu.Unlock()
	if ch == nil {
		t.Fatal("Send must allocate s.turnDone before dispatching to the codex app-server runner")
	}
	select {
	case <-ch:
	default:
		t.Fatal("turnDone must be signalable by the turn's result event")
	}
}
