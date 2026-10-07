package agentproxy

import (
	"context"
	"log/slog"
	"time"
)

// defaultTurnInactivityTimeout bounds how long a single turn may produce NO
// output at all before the watchdog treats the long-lived process as
// wedged/lost and acts. A working agent streams events continuously (text /
// tool_use / tool_result / stream_event), so this measures INACTIVITY, not
// total turn duration — a legitimately long turn (e.g. a 700s build with
// steady output) keeps resetting the clock and is never killed, while a
// process that has gone silent (hung on a tool/MCP/network call, CLI
// deadlock, or simply lost) is acted on so the turn can recover instead of
// blocking forever. Overridable per-session via
// WorkspaceSession.turnInactivityTimeout: the override drives BOTH the
// threshold and the polling cadence.
const defaultTurnInactivityTimeout = 15 * time.Minute

// toolInactivityGrace is how long a SINGLE in-flight tool invocation may stay
// silent before the turn watchdog judges it hung. Long builds / test suites
// legitimately produce zero stream output for far longer than the base
// window; a tool running past this ceiling is itself considered wedged and
// the turn fails into the normal error path.
const toolInactivityGrace = time.Hour

// turnInactivityExceeded is the single watchdog judgment shared by every
// engine: has this turn produced no output for long enough to act?
// Base rule: no output within `window`. Grace rule: while a tool is in
// flight the judgment extends to the tool's own start + toolInactivityGrace —
// a 30-minute build or test run is legitimate work, but a tool silent past
// the grace ceiling is itself wedged and the turn should fail.
func turnInactivityExceeded(s *WorkspaceSession, window, grace time.Duration, now time.Time) bool {
	s.mu.Lock()
	idle := now.Sub(s.lastActivityAt)
	toolAt := s.toolInProgressAt
	s.mu.Unlock()
	if idle < window {
		return false // fresh output — a streaming turn, never exceeded
	}
	if !toolAt.IsZero() && now.Sub(toolAt) < grace {
		return false // a tool is running; extend to its own grace ceiling
	}
	return true
}

// watchdogTurnInactivity is THE turn inactivity watchdog every engine shares
// — one definition, one implementation. It polls every window/5 and, when
// turnInactivityExceeded judges the turn wedged, calls onFire exactly once
// with the idle duration and returns. It returns early (without firing) when
// done closes — the turn completed or the host cancelled. Engines differ
// only in the ACTION, never in the judgment: the line-based waiter
// (waitForTurnComplete) marks the turn failed and kills the process; the
// cancel-style protocol engines (watchBackendTurnInactivity,
// watchOneShotInactivity) cancel the turn context, which tears the backend
// down through its own Abort/EOF path.
func watchdogTurnInactivity(s *WorkspaceSession, done <-chan struct{}, window time.Duration, label string, onFire func(idle time.Duration)) {
	ticker := time.NewTicker(window / 5)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if !turnInactivityExceeded(s, window, toolInactivityGrace, time.Now()) {
				continue // still streaming, or a tool is legitimately running
			}
			s.mu.Lock()
			idle := time.Since(s.lastActivityAt)
			s.mu.Unlock()
			slog.Error(label+": turn inactivity watchdog — no output within "+window.String()+", acting on unresponsive agent",
				"workspace_id", s.workspaceID, "idle", idle.String())
			onFire(idle)
			return
		}
	}
}

// watchBackendTurnInactivity adapts the shared watchdog for the
// protocol-backend engines (omp / goose / cursor / niuniu-agent): the action
// is cancelling the turn ctx — the backend's Abort-on-done tears the
// subprocess down. Replaces the old hard turn caps that killed legitimate
// long turns mid-flight.
func watchBackendTurnInactivity(s *WorkspaceSession, cancel context.CancelFunc, turnDone <-chan struct{}, window time.Duration) {
	watchdogTurnInactivity(s, turnDone, window, "agent backend turn", func(time.Duration) { cancel() })
}

// watchOneShotInactivity adapts the shared watchdog for the one-shot runner
// (no turnDone channel, no separate waiter): the action is cancelling
// cmdCtx, which kills the process, EOFs the scanner, and unblocks the turn
// into the normal error path.
func watchOneShotInactivity(s *WorkspaceSession, cancel context.CancelFunc, done <-chan struct{}, window time.Duration) {
	watchdogTurnInactivity(s, done, window, "agent one-shot", func(time.Duration) { cancel() })
}
