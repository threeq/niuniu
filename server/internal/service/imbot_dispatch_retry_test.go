package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/imbot"
)

// Outbound push retry (issue #681). Before this, a failed push was logged and
// forgotten: a platform rate limit, a briefly-expired token or a network blip
// PERMANENTLY lost an agent's reply, and the user saw a task that simply never
// answered.

// flakyAdapter fails a configurable number of pushes before succeeding, recording
// every attempt. failWith lets a test choose the error's classification.
type flakyAdapter struct {
	recordAdapter
	failMu    sync.Mutex
	failsLeft int
	failWith  error
	attempts  int
}

func (a *flakyAdapter) Push(ctx context.Context, cred imbot.Credential, msg imbot.OutboundMessage) error {
	a.failMu.Lock()
	a.attempts++
	if a.failsLeft > 0 {
		a.failsLeft--
		err := a.failWith
		a.failMu.Unlock()
		return err
	}
	a.failMu.Unlock()
	return a.recordAdapter.Push(ctx, cred, msg)
}

func (a *flakyAdapter) attemptCount() int {
	a.failMu.Lock()
	defer a.failMu.Unlock()
	return a.attempts
}

// newRetryDispatcher wires a dispatcher whose backoff does not actually wait, so a
// retry schedule is exercised at test speed rather than in real seconds.
func newRetryDispatcher(bus *event.Bus, f *imbotFixture, ad imbot.ChannelAdapter) *IMBotDispatcher {
	d := NewIMBotDispatcher(bus, f.q, f.svc, map[imbot.ChannelType]imbot.ChannelAdapter{
		imbot.ChannelLark: ad,
	})
	d.retrySleep = func(_ time.Duration, stop <-chan struct{}) bool {
		select {
		case <-stop:
			return false
		default:
			return true
		}
	}
	return d
}

// waitForAttempts polls a flaky adapter until it has seen n attempts.
func waitForAttempts(a *flakyAdapter, n int) int {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := a.attemptCount(); got >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	return a.attemptCount()
}

// TestDispatcher_RetriesTransientPushFailure is the core of #681: a 429 must not
// cost the user their reply.
func TestDispatcher_RetriesTransientPushFailure(t *testing.T) {
	f := newIMBotFixture(t)
	chat := f.activeChat(t, "oc_retry")
	f.setActiveIssueForTest(t, chat, f.issueID)

	ad := &flakyAdapter{
		failsLeft: 2,
		failWith:  imbot.NewPushError(http.StatusTooManyRequests, errors.New("lark: send failed status=429")),
	}
	bus := event.NewBus()
	d := newRetryDispatcher(bus, f, ad)
	d.Start()
	defer d.Stop()

	bus.Publish(event.OutputEvent{Type: event.EventAgentDone, Content: "重要回复", WorkspaceId: f.wsID})

	pushes := waitForPushes(&ad.recordAdapter, 1)
	if len(pushes) != 1 {
		t.Fatalf("message never delivered after retries; pushes=%d attempts=%d",
			len(pushes), ad.attemptCount())
	}
	if !strings.Contains(pushes[0].Text, "重要回复") {
		t.Errorf("delivered text = %q, want the original reply", pushes[0].Text)
	}
	if got := ad.attemptCount(); got != 3 {
		t.Errorf("attempts = %d, want 3 (two failures then success)", got)
	}
}

// TestDispatcher_DoesNotRetryPermanentFailure: a 401 fails identically forever.
// Retrying wastes quota and, for auth errors, can read as credential brute-forcing
// to platform risk controls.
func TestDispatcher_DoesNotRetryPermanentFailure(t *testing.T) {
	f := newIMBotFixture(t)
	chat := f.activeChat(t, "oc_perm")
	f.setActiveIssueForTest(t, chat, f.issueID)

	ad := &flakyAdapter{
		failsLeft: 5, // would keep failing if retried
		failWith:  imbot.NewPushError(http.StatusUnauthorized, errors.New("lark: send failed status=401")),
	}
	bus := event.NewBus()
	d := newRetryDispatcher(bus, f, ad)
	d.Start()
	defer d.Stop()

	bus.Publish(event.OutputEvent{Type: event.EventAgentDone, Content: "reply", WorkspaceId: f.wsID})

	if got := waitForAttempts(ad, 1); got != 1 {
		t.Errorf("attempts = %d, want exactly 1 for a permanent failure", got)
	}
	if pushes := snapshotPushes(&ad.recordAdapter); len(pushes) != 0 {
		t.Errorf("permanent failure delivered %d messages, want 0", len(pushes))
	}
}

// TestDispatcher_GivesUpAfterMaxAttempts: retries are bounded. A message held
// forever would arrive detached from the conversation it belongs to, and an
// unbounded queue is its own failure mode.
func TestDispatcher_GivesUpAfterMaxAttempts(t *testing.T) {
	f := newIMBotFixture(t)
	chat := f.activeChat(t, "oc_giveup")
	f.setActiveIssueForTest(t, chat, f.issueID)

	ad := &flakyAdapter{
		failsLeft: 99,
		failWith:  imbot.NewPushError(http.StatusInternalServerError, errors.New("lark: send failed status=500")),
	}
	bus := event.NewBus()
	d := newRetryDispatcher(bus, f, ad)
	d.Start()
	defer d.Stop()

	bus.Publish(event.OutputEvent{Type: event.EventAgentDone, Content: "reply", WorkspaceId: f.wsID})

	if got := waitForAttempts(ad, pushMaxAttempts); got != pushMaxAttempts {
		t.Errorf("attempts = %d, want the cap %d", got, pushMaxAttempts)
	}
}

// TestDispatcher_UnclassifiedErrorIsRetried: an adapter that does not classify its
// failures (WeChat's iLink ret codes conflate rate-limiting with token expiry) must
// still get retries — losing a reply to a blip is the failure this fix exists to
// stop, and it is more common than a permanent error worth abandoning at once.
func TestDispatcher_UnclassifiedErrorIsRetried(t *testing.T) {
	f := newIMBotFixture(t)
	chat := f.activeChat(t, "oc_plain")
	f.setActiveIssueForTest(t, chat, f.issueID)

	ad := &flakyAdapter{failsLeft: 1, failWith: errors.New("wechat: sendmessage ret=-2")}
	bus := event.NewBus()
	d := newRetryDispatcher(bus, f, ad)
	d.Start()
	defer d.Stop()

	bus.Publish(event.OutputEvent{Type: event.EventAgentDone, Content: "reply", WorkspaceId: f.wsID})

	if pushes := waitForPushes(&ad.recordAdapter, 1); len(pushes) != 1 {
		t.Fatalf("unclassified error was not retried; pushes=%d attempts=%d",
			len(pushes), ad.attemptCount())
	}
}

// TestDispatcher_PreservesPerChatOrderAcrossRetry: messages to ONE chat must arrive
// in the order they were produced. A group reading a completion notice before the
// answer it refers to is worse than a slow answer, so a retried message must not be
// overtaken by the next one.
func TestDispatcher_PreservesPerChatOrderAcrossRetry(t *testing.T) {
	f := newIMBotFixture(t)
	chat := f.activeChat(t, "oc_order")
	f.setActiveIssueForTest(t, chat, f.issueID)

	ad := &flakyAdapter{
		failsLeft: 1,
		failWith:  imbot.NewPushError(http.StatusTooManyRequests, errors.New("429")),
	}
	bus := event.NewBus()
	d := newRetryDispatcher(bus, f, ad)
	d.Start()
	defer d.Stop()

	bus.Publish(event.OutputEvent{Type: event.EventAgentDone, Content: "first", WorkspaceId: f.wsID})
	// Wait for the first message to land before publishing the second, so the test
	// asserts ordering rather than racing the bus.
	if got := waitForPushes(&ad.recordAdapter, 1); len(got) != 1 {
		t.Fatalf("first message not delivered: %d pushes", len(got))
	}
	bus.Publish(event.OutputEvent{Type: event.EventAgentDone, Content: "second", WorkspaceId: f.wsID})

	pushes := waitForPushes(&ad.recordAdapter, 2)
	if len(pushes) != 2 {
		t.Fatalf("expected 2 delivered messages, got %d", len(pushes))
	}
	if !strings.Contains(pushes[0].Text, "first") || !strings.Contains(pushes[1].Text, "second") {
		t.Errorf("messages arrived out of order: %q then %q", pushes[0].Text, pushes[1].Text)
	}
}

// TestDispatcher_StopDrainsSenders: shutdown must not race an in-flight push, and
// must not block for a full retry schedule either — sendWithRetry abandons its
// backoff when stop closes.
func TestDispatcher_StopDrainsSenders(t *testing.T) {
	f := newIMBotFixture(t)
	chat := f.activeChat(t, "oc_stop")
	f.setActiveIssueForTest(t, chat, f.issueID)

	ad := &flakyAdapter{
		failsLeft: 99,
		failWith:  imbot.NewPushError(http.StatusInternalServerError, errors.New("500")),
	}
	bus := event.NewBus()
	d := newRetryDispatcher(bus, f, ad)
	d.Start()
	bus.Publish(event.OutputEvent{Type: event.EventAgentDone, Content: "reply", WorkspaceId: f.wsID})
	waitForAttempts(ad, 1)

	done := make(chan struct{})
	go func() {
		d.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return; a sender is not observing the stop channel")
	}
}

// TestPushError_Classification pins the retry/permanent boundary directly, since
// every retry decision routes through it.
func TestPushError_Classification(t *testing.T) {
	cases := []struct {
		status    int
		retryable bool
	}{
		{0, true},                              // never reached the platform
		{http.StatusTooManyRequests, true},     // 429 — slow down
		{http.StatusRequestTimeout, true},      // 408
		{http.StatusInternalServerError, true}, // 5xx — platform trouble
		{http.StatusBadGateway, true},          // 502
		{http.StatusUnauthorized, false},       // 401 — credentials
		{http.StatusForbidden, false},          // 403
		{http.StatusBadRequest, false},         // 400 — malformed
		{http.StatusNotFound, false},           // 404 — chat/thread gone
	}
	for _, c := range cases {
		err := imbot.NewPushError(c.status, errors.New("boom"))
		if got := imbot.IsRetryablePush(err); got != c.retryable {
			t.Errorf("status %d: retryable = %v, want %v", c.status, got, c.retryable)
		}
	}
	if imbot.IsRetryablePush(nil) {
		t.Error("nil error must not be retryable")
	}
	// NewPushError must be nil-safe so call sites can wrap unconditionally.
	if imbot.NewPushError(500, nil) != nil {
		t.Error("NewPushError(_, nil) must be nil")
	}
	// The underlying message must survive wrapping — logs depend on it.
	if got := imbot.NewPushError(429, errors.New("lark: send failed")).Error(); got != "lark: send failed" {
		t.Errorf("wrapped error message = %q, want the original", got)
	}
}

// TestDispatcher_RestartAfterStop: Stop nils the senders map, so a later Start must
// build fresh senders bound to the NEW stop channel. A sender left over from the
// previous run would be watching a closed channel and exit immediately, silently
// dropping every message for that chat.
func TestDispatcher_RestartAfterStop(t *testing.T) {
	f := newIMBotFixture(t)
	chat := f.activeChat(t, "oc_restart")
	f.setActiveIssueForTest(t, chat, f.issueID)

	ad := &flakyAdapter{}
	bus := event.NewBus()
	d := newRetryDispatcher(bus, f, ad)

	d.Start()
	bus.Publish(event.OutputEvent{Type: event.EventAgentDone, Content: "first-run", WorkspaceId: f.wsID})
	if got := waitForPushes(&ad.recordAdapter, 1); len(got) != 1 {
		t.Fatalf("first run delivered %d messages, want 1", len(got))
	}
	d.Stop()

	// Second run must deliver too.
	d.Start()
	defer d.Stop()
	bus.Publish(event.OutputEvent{Type: event.EventAgentDone, Content: "second-run", WorkspaceId: f.wsID})
	pushes := waitForPushes(&ad.recordAdapter, 2)
	if len(pushes) != 2 {
		t.Fatalf("after restart delivered %d messages total, want 2", len(pushes))
	}
	if !strings.Contains(pushes[1].Text, "second-run") {
		t.Errorf("second message = %q, want the post-restart reply", pushes[1].Text)
	}
}
