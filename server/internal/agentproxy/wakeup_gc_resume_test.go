package agentproxy

import (
	"context"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// wakeupGCSession is a session whose live state (running) starts false; the
// shared fakeStatusHook (proxy_dispatch_test.go) records status transitions.
type wakeupGCSession struct {
	*WorkspaceSession
	hook *fakeStatusHook
	db   *store.DB
}

// Regression (team edition, workspace stuck "running"): an agent's
// ScheduleWakeup defers finalize — finalizeSendLoopTurn keeps agent_status
// 'running' while a future wakeup is pending, and Enqueue queues messages
// behind it, BOTH on the promise that the wakeup's resume will re-enter
// SendLoop and drain. Nothing did: gcInflightLoop's GCStale silently deleted
// the expired wakeup, so the queue drained NEVER and the workspace stayed
// "running" forever. These tests pin the resume contract.

func newWakeupGCSession(t *testing.T) *wakeupGCSession {
	t.Helper()
	s, db := newDispatchTestSessionWithDB(t)
	s.hub = NewSessionHub()
	t.Cleanup(s.hub.Stop)
	s.inflight = NewInflightTracker()
	s.workDir = t.TempDir()
	hook := &fakeStatusHook{}
	s.statusHook = hook
	return &wakeupGCSession{WorkspaceSession: s, hook: hook, db: db}
}

// TestGCCollectsExpiredWakeupAndDrainsQueue: expired wakeup + a queued message
// + no live loop → the GC must re-enter a SendLoop (which drains the queue).
func TestGCCollectsExpiredWakeupAndDrainsQueue(t *testing.T) {
	f := newWakeupGCSession(t)
	s := f.WorkspaceSession
	ctx := context.Background()

	// A FUTURE wakeup (that's what makes Enqueue queue) + one queued message.
	// No live loop.
	s.inflight.AddWakeup("tu_w", "check later", time.Now().Add(-time.Minute), 2*time.Minute) // ScheduledFor ≈ +1min
	if _, _, err := s.Enqueue(ctx, "跟进一下结果", ""); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if s.PendingCount(ctx) != 1 {
		t.Fatalf("queue precondition: %d pending, want 1", s.PendingCount(ctx))
	}
	s.mu.Lock()
	running := s.running
	s.mu.Unlock()
	if running {
		t.Fatal("precondition: no live loop expected")
	}

	p := &AgentProxy{sessions: map[int64]*WorkspaceSession{s.workspaceID: s}, hub: s.hub}
	p.gcSessionInflight(s, time.Now().Add(3*time.Minute)) // wakeup now expired

	// The resume must take over the queued message: the GC dequeues it and
	// hands it to a fresh SendLoop (whose own dequeue drains the rest). The
	// observable contract here is the queue being taken over — the message no
	// longer sits stranded in 排队.
	if got := s.PendingCount(ctx); got != 0 {
		t.Fatalf("queued message must be taken over by the resume, %d still pending", got)
	}
}

// TestGCCollectsExpiredWakeupFlipsStatusWhenIdle: expired wakeup + empty queue
// + no live loop → nothing will ever re-enter the workspace, so the GC must
// run the finalize tail (status hook done) that the deferred finalize skipped —
// otherwise agent_status stays 'running' forever.
func TestGCCollectsExpiredWakeupFlipsStatusWhenIdle(t *testing.T) {
	f := newWakeupGCSession(t)
	s := f.WorkspaceSession
	hook := f.hook

	s.inflight.AddWakeup("tu_w", "check later", time.Now().Add(-time.Minute), 2*time.Minute) // ScheduledFor ≈ +1min

	p := &AgentProxy{sessions: map[int64]*WorkspaceSession{s.workspaceID: s}, hub: s.hub}
	p.gcSessionInflight(s, time.Now().Add(3*time.Minute)) // wakeup now expired

	if len(hook.events) != 1 || hook.events[0] != "done" {
		t.Fatalf("status hook events = %v, want exactly [done] — expired wakeup must flip the stale 'running' state", hook.events)
	}
	// The wakeup entry is gone.
	if s.hasPendingFutureWakeup() {
		t.Fatal("expired wakeup must be collected")
	}
}

// TestGCLeavesLiveLoopAlone: with a live loop the GC must not start a second
// one nor flip status — the loop owns the lifecycle.
func TestGCLeavesLiveLoopAlone(t *testing.T) {
	f := newWakeupGCSession(t)
	s := f.WorkspaceSession
	hook := f.hook
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	s.inflight.AddWakeup("tu_w", "later", time.Now().Add(-time.Minute), 2*time.Minute) // ScheduledFor ≈ +1min

	p := &AgentProxy{sessions: map[int64]*WorkspaceSession{s.workspaceID: s}, hub: s.hub}
	p.gcSessionInflight(s, time.Now().Add(3*time.Minute)) // wakeup now expired

	if len(hook.events) != 0 {
		t.Fatalf("status hook must not fire while a live loop owns the workspace, got %v", hook.events)
	}
}
