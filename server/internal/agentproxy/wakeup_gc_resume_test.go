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

// fakeWaitSched is a RateLimitScheduler stub whose HasArmedAutoResume is
// switchable, so tests can model "a live resume backs the wait flag" vs "the
// promise is dead".
type fakeWaitSched struct{ armed bool }

func (f *fakeWaitSched) OnRateLimited(ctx context.Context, workspaceID int64, resetsAtUnix int64) {}
func (f *fakeWaitSched) OnAutohostWait(ctx context.Context, workspaceID int64, runAtUnix int64, message string) bool {
	return true
}
func (f *fakeWaitSched) CancelAutoResume(ctx context.Context, workspaceID int64) {}
func (f *fakeWaitSched) HasArmedAutoResume(ctx context.Context, workspaceID int64) bool {
	return f.armed
}

// Regression (长任务卡「运行中」): scheduleBgWaitResume sets autohostScheduledWait
// and promises a scheduler resume will re-enter SendLoop. When that promise
// dies (its one-shot timer was consumed by a busy-skip, lost to a restart, or
// never registered), the flag keeps finalize deferred (badge stuck 运行中) and
// the Enqueue gate closed (every message stuck in 排队) FOREVER — nothing else
// re-checks it. The GC must verify the promise and, when dead, clear the flag
// and run the finalize tail the deferred finalize skipped.
func TestGCClearsDeadAutohostWaitPromiseAndFinalizes(t *testing.T) {
	f := newWakeupGCSession(t)
	s := f.WorkspaceSession
	hook := f.hook

	s.mu.Lock()
	s.autohostScheduledWait = true
	s.mu.Unlock()

	// parent without a rateLimitSched → no armed resume can exist. No live
	// loop, empty queue, nothing in inflight — exactly the stranded shape.
	p := &AgentProxy{sessions: map[int64]*WorkspaceSession{s.workspaceID: s}, hub: s.hub}
	s.parent = p
	p.gcSessionInflight(s, time.Now())

	if len(hook.events) != 1 || hook.events[0] != "done" {
		t.Fatalf("status hook events = %v, want exactly [done] — a dead wait promise must not hold the workspace running", hook.events)
	}
	s.mu.Lock()
	waiting := s.autohostScheduledWait
	s.mu.Unlock()
	if waiting {
		t.Fatal("dead promise: autohostScheduledWait must be cleared")
	}
}

// Same dead promise, but the user queued a message meanwhile: clearing the
// flag must also unstick the queue — the GC drains the first message into a
// fresh SendLoop instead of finalizing over a non-empty queue.
func TestGCDeadPromiseDrainsQueue(t *testing.T) {
	f := newWakeupGCSession(t)
	s := f.WorkspaceSession
	hook := f.hook
	ctx := context.Background()

	s.mu.Lock()
	s.autohostScheduledWait = true
	s.mu.Unlock()
	if _, _, err := s.Enqueue(ctx, "继续做后面的任务", ""); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	p := &AgentProxy{sessions: map[int64]*WorkspaceSession{s.workspaceID: s}, hub: s.hub}
	s.parent = p
	p.gcSessionInflight(s, time.Now())

	if got := s.PendingCount(ctx); got != 0 {
		t.Fatalf("queued message must be taken over once the dead promise is cleared, %d still pending", got)
	}
	if len(hook.events) != 0 {
		t.Fatalf("must resume into a loop, not finalize, when the queue has work — got %v", hook.events)
	}
}

// The happy-path guard: when a live resume IS armed behind the wait flag, the
// GC must keep deferring to it (the workspace legitimately stays running).
func TestGCArmedAutohostWaitStaysRunning(t *testing.T) {
	f := newWakeupGCSession(t)
	s := f.WorkspaceSession
	hook := f.hook

	s.mu.Lock()
	s.autohostScheduledWait = true
	s.mu.Unlock()

	p := &AgentProxy{
		sessions:       map[int64]*WorkspaceSession{s.workspaceID: s},
		hub:            s.hub,
		rateLimitSched: &fakeWaitSched{armed: true},
	}
	s.parent = p
	p.gcSessionInflight(s, time.Now())

	if len(hook.events) != 0 {
		t.Fatalf("an armed resume owns re-entry — GC must not finalize, got %v", hook.events)
	}
}
