package agentproxy

import (
	"context"
	"strings"
	"testing"
	"time"
)

// newWatchdogSession builds the minimal WorkspaceSession needed to exercise the
// turn-wait / watchdog / kill paths. None of these touch q/hub, so they're left
// nil — killProcess on a nil cmd is a no-op (but still signals turnDone).
func newWatchdogSession(window time.Duration) *WorkspaceSession {
	return &WorkspaceSession{
		turnDone:              make(chan struct{}, 1),
		turnInactivityTimeout: window,
	}
}

// TestWaitForTurnComplete_ReturnsOnTurnDone: a normal turn (result event signals
// turnDone) unblocks the wait with no error.
func TestWaitForTurnComplete_ReturnsOnTurnDone(t *testing.T) {
	s := newWatchdogSession(time.Minute) // window far larger than the test
	s.lastActivityAt = time.Now()

	go func() {
		time.Sleep(20 * time.Millisecond)
		s.mu.Lock()
		ch := s.turnDone
		s.mu.Unlock()
		ch <- struct{}{}
	}()

	if err := s.waitForTurnComplete(context.Background(), "test"); err != nil {
		t.Fatalf("waitForTurnComplete returned error on normal completion: %v", err)
	}
}

// TestWaitForTurnComplete_WatchdogKillsWedgedProcess is the core regression for
// the stuck-workspace bug: a process that is alive but produces NO output (never
// signals turnDone, never exits) must be reaped by the inactivity watchdog
// instead of blocking forever. The wait must return an error AND flag the turn
// as an error so SendLoop recovers rather than silently autohost-continuing.
func TestWaitForTurnComplete_WatchdogKillsWedgedProcess(t *testing.T) {
	s := newWatchdogSession(60 * time.Millisecond)
	// Seed activity well in the past so the very first tick trips the watchdog.
	s.lastActivityAt = time.Now().Add(-time.Hour)

	start := time.Now()
	err := s.waitForTurnComplete(context.Background(), "test")
	if err == nil {
		t.Fatal("watchdog did not fire on a silent process — wait returned nil")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("watchdog took too long to fire: %s", elapsed)
	}
	if !strings.Contains(err.Error(), "watchdog") {
		t.Errorf("error = %q, want it to mention the watchdog", err.Error())
	}
	s.mu.Lock()
	gotErr, gotResult := s.lastTurnError, s.lastTurnResult
	s.mu.Unlock()
	if !gotErr {
		t.Error("watchdog did not set lastTurnError=true; SendLoop would treat the dead turn as clean and autohost-continue")
	}
	if !strings.Contains(gotResult, "unresponsive") {
		t.Errorf("lastTurnResult = %q, want an 'unresponsive' explanation", gotResult)
	}
}

// TestWaitForTurnComplete_LongTurnNotKilled: a turn that keeps streaming output
// (lastActivityAt bumped continuously) must NOT be killed even though it runs
// far longer than the inactivity window — distinguishing "working long" from
// "wedged".
func TestWaitForTurnComplete_LongTurnNotKilled(t *testing.T) {
	window := 80 * time.Millisecond
	s := newWatchdogSession(window)
	s.lastActivityAt = time.Now()

	done := make(chan error, 1)
	go func() { done <- s.waitForTurnComplete(context.Background(), "test") }()

	// Bump activity faster than the window for ~3 windows, then complete cleanly.
	stop := time.After(3 * window)
	ticker := time.NewTicker(window / 4)
	defer ticker.Stop()
loop:
	for {
		select {
		case <-ticker.C:
			s.mu.Lock()
			s.lastActivityAt = time.Now()
			s.mu.Unlock()
		case <-stop:
			break loop
		}
	}
	s.mu.Lock()
	ch := s.turnDone
	s.mu.Unlock()
	ch <- struct{}{} // turn completes cleanly

	if err := <-done; err != nil {
		t.Fatalf("long streaming turn was killed by the watchdog: %v", err)
	}
}

// TestKillProcess_UnblocksWaiter is the regression for "Stop doesn't work":
// killProcess must signal turnDone directly so a blocked wait (Send) returns
// immediately, instead of depending on the process-monitor goroutine's race.
func TestKillProcess_UnblocksWaiter(t *testing.T) {
	s := newWatchdogSession(time.Minute) // window large — only the kill should unblock

	done := make(chan error, 1)
	go func() { done <- s.waitForTurnComplete(context.Background(), "test") }()

	time.Sleep(20 * time.Millisecond) // let the waiter park on turnDone
	s.killProcess()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("waiter returned error after killProcess, want nil unblock: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("killProcess did not unblock the waiter within 2s — Stop would be a no-op")
	}
}
