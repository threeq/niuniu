package agentproxy

import (
	"context"
	"testing"
	"time"
)

// --- isTransientStreamError: which failed turns deserve an automatic re-run ---

// TestIsTransientStreamError pins the classifier for auto-retryable stream
// failures. These are mid-stream network/gateway stalls the CLI reports as a
// fatal turn error WITHOUT its own retry (unlike 429/5xx which surface as
// api_retry events). Production case: batch_create_issues' multi-KB tool_use
// JSON made the 火山方舟 glm gateway stall; exactly 180s later the CLI gave up
// with "Response stalled mid-stream", SendLoop dropped to attention, and the
// user had to re-send the batch by hand every time.
func TestIsTransientStreamError(t *testing.T) {
	cases := []struct {
		name   string
		result string
		want   bool
	}{
		{"stalled mid-stream", "API Error: Response stalled mid-stream. The response above may be incomplete.", true},
		{"connection refused", "API Error: Unable to connect to API (ConnectionRefused)", true},
		{"watchdog kill", "agent unresponsive: produced no output within the watchdog window; the process was killed and will restart", true},
		{"overloaded", "API Error: 529 Overloaded", true},
		{"rate limit text is NOT transient here (429 has its own fallback path)", "Request rejected (429) · resets at 15:00", false},
		{"genuine refusal", "API Error: 400 invalid_request_error", false},
		{"ordinary tool failure text", "exit status 1: build failed", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		if got := isTransientStreamError(tc.result); got != tc.want {
			t.Errorf("%s: isTransientStreamError(%q) = %v, want %v", tc.name, tc.result, got, tc.want)
		}
	}
}

// --- SendLoop integration: a transient stream error re-runs the SAME message --

// TestSendLoopAutoRetriesTransientStreamError drives a full SendLoop against a
// fake CLI whose first turn ends with the stalled-stream error result and whose
// second turn completes cleanly. The loop must re-run the SAME content (a
// system-rendered retry, not a new "You" bubble) instead of stopping into
// attention, and must reset the retry counter after the clean turn.
func TestSendLoopAutoRetriesTransientStreamError(t *testing.T) {
	s := newTransientRetrySession(t)
	turn := 0
	s.turnResultOverride = func() (string, bool) {
		turn++
		if turn == 1 {
			return "API Error: Response stalled mid-stream. The response above may be incomplete.", true
		}
		return "done", false
	}

	done := make(chan struct{})
	go func() { s.SendLoop(context.Background(), s.workDirForTest(), "批量建 5 个子 issue", ""); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("SendLoop did not return")
	}

	s.mu.Lock()
	retries := s.transientRetries
	s.mu.Unlock()
	if retries != 0 {
		t.Fatalf("retry counter must reset after a clean turn, got %d", retries)
	}
	// The loop must NOT have flagged attention: the second turn was clean.
	s.mu.Lock()
	lastErr := s.lastTurnError
	s.mu.Unlock()
	if lastErr {
		t.Fatal("clean second turn must clear lastTurnError")
	}
}

// TestSendLoopTransientRetryBudgetExhausted stops the loop into attention after
// the retry budget is spent — a gateway that stalls on EVERY attempt must not
// retry forever.
func TestSendLoopTransientRetryBudgetExhausted(t *testing.T) {
	s := newTransientRetrySession(t)
	s.turnResultOverride = func() (string, bool) {
		return "API Error: Response stalled mid-stream.", true
	}

	done := make(chan struct{})
	go func() { s.SendLoop(context.Background(), s.workDirForTest(), "批量建 5 个子 issue", ""); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("SendLoop did not return")
	}

	s.mu.Lock()
	retries := s.transientRetries
	s.mu.Unlock()
	if retries != maxTransientStreamRetries {
		t.Fatalf("retries = %d, want the budget %d", retries, maxTransientStreamRetries)
	}
}

// --- helpers ------------------------------------------------------------------

// newTransientRetrySession builds the minimal session SendLoop needs: the fake
// process lifecycle is stubbed at ensureProcess/waitForTurnComplete level via
// turnResultOverride (set by tests); no real CLI is spawned.
func newTransientRetrySession(t *testing.T) *WorkspaceSession {
	t.Helper()
	s := newDispatchTestSession(t)
	s.hub = NewSessionHub()
	t.Cleanup(s.hub.Stop)
	return s
}

// workDirForTest returns an existing directory for SendLoop's workDir arg.
func (s *WorkspaceSession) workDirForTest() string {
	return "."
}
