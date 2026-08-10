package notify

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeHub struct {
	mu        sync.Mutex
	count     atomic.Int64
	lastTopic string
}

func (h *fakeHub) record(n Notification) {
	h.mu.Lock()
	h.lastTopic = n.Topic
	h.mu.Unlock()
	h.count.Add(1)
}

// staticResolver returns a resolver that always returns the given ownerType and ownerID with ok=true.
func staticResolver(ownerType string, ownerID int64) func() (string, int64, bool) {
	return func() (string, int64, bool) { return ownerType, ownerID, true }
}

func TestBgTaskDebouncer_CoalescesBurst(t *testing.T) {
	h := &fakeHub{}
	d := NewBgTaskDebouncer(50*time.Millisecond, h.record)
	for i := 0; i < 10; i++ {
		d.Notify(42, staticResolver("user", 1))
	}
	time.Sleep(120 * time.Millisecond)
	if got := h.count.Load(); got != 1 {
		t.Fatalf("expected 1 emit after burst, got %d", got)
	}
	if h.lastTopic != TopicWorkspaceBgTask {
		t.Fatalf("expected topic %q, got %q", TopicWorkspaceBgTask, h.lastTopic)
	}
}

func TestBgTaskDebouncer_SeparateAfterWindow(t *testing.T) {
	h := &fakeHub{}
	d := NewBgTaskDebouncer(30*time.Millisecond, h.record)
	d.Notify(42, staticResolver("user", 1))
	time.Sleep(80 * time.Millisecond)
	d.Notify(42, staticResolver("user", 1))
	time.Sleep(80 * time.Millisecond)
	if got := h.count.Load(); got != 2 {
		t.Fatalf("expected 2 emits with gap > delay, got %d", got)
	}
}

func TestBgTaskDebouncer_PerWorkspaceIndependent(t *testing.T) {
	h := &fakeHub{}
	d := NewBgTaskDebouncer(40*time.Millisecond, h.record)
	d.Notify(1, staticResolver("user", 1))
	d.Notify(2, staticResolver("user", 2))
	time.Sleep(100 * time.Millisecond)
	if got := h.count.Load(); got != 2 {
		t.Fatalf("expected 2 emits (one per workspace), got %d", got)
	}
}

func TestBgTaskDebouncer_ResolverFalse_NoEmit(t *testing.T) {
	h := &fakeHub{}
	d := NewBgTaskDebouncer(30*time.Millisecond, h.record)
	d.Notify(42, func() (string, int64, bool) { return "", 0, false })
	time.Sleep(80 * time.Millisecond)
	if got := h.count.Load(); got != 0 {
		t.Fatalf("expected no emits when resolver returns false, got %d", got)
	}
}

func TestBgTaskDebouncer_EmptyOwnerType_NoEmit(t *testing.T) {
	h := &fakeHub{}
	d := NewBgTaskDebouncer(30*time.Millisecond, h.record)
	d.Notify(42, func() (string, int64, bool) { return "", 1, true })
	time.Sleep(80 * time.Millisecond)
	if got := h.count.Load(); got != 0 {
		t.Fatalf("expected no emits when ownerType is empty, got %d", got)
	}
}
