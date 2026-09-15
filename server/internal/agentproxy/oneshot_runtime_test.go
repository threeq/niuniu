package agentproxy

import (
	"testing"
	"time"
)

// TestWatchOneShotInactivity_KillsWedgedProcess pins the one-shot inactivity
// watchdog: a process that has produced NO output for the whole window must
// have its cmdCtx cancelled (which kills it and EOFs the scanner), so the
// turn fails into the normal error path instead of hanging forever and
// queueing every later message. A streaming turn (fresh activity) must never
// be cancelled.
func TestWatchOneShotInactivity_KillsWedgedProcess(t *testing.T) {
	s := newDispatchTestSession(t)
	cancelled := make(chan struct{})
	cancel := func() { close(cancelled) }

	// Stale activity: last output well past the window.
	s.mu.Lock()
	s.lastActivityAt = time.Now().Add(-time.Hour)
	s.mu.Unlock()

	done := make(chan struct{})
	go watchOneShotInactivity(s, cancel, done, 50*time.Millisecond)

	select {
	case <-cancelled:
		// watchdog fired as expected
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog never cancelled a wedged one-shot process")
	}
	close(done)
}

func TestWatchOneShotInactivity_SparedWhenStreaming(t *testing.T) {
	s := newDispatchTestSession(t)
	cancelled := make(chan struct{})
	cancel := func() { close(cancelled) }

	// Fresh activity: a long streaming turn must NOT be killed.
	s.mu.Lock()
	s.lastActivityAt = time.Now()
	s.mu.Unlock()

	done := make(chan struct{})
	go watchOneShotInactivity(s, cancel, done, 80*time.Millisecond)

	// Keep the turn "streaming" for longer than the window.
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case <-cancelled:
			t.Fatal("watchdog killed a streaming turn")
		case <-deadline:
			close(done)
			return
		case <-time.After(20 * time.Millisecond):
			s.mu.Lock()
			s.lastActivityAt = time.Now()
			s.mu.Unlock()
		}
	}
}
