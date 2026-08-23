package agentproxy

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// Regression suite for the "scheduled agent never exits" memory-death bug
// (self.niu6ai.com 2026-08-23): a schedule firing every 15min < idleTimeout
// (30min) reset the idle reaper on every turn, so the long-lived claude process
// lived forever, its --resume session grew monotonically, and on a 3.4GB box
// the accumulated agents thrashed swap until the host hard-froze.
//
// Fix shape: a SendLoop started by the SCHEDULER (DeliverFromScheduler) is
// one-shot by semantics — when the loop ends cleanly (queue empty, no autohost
// continue, no scheduled wait), it must reap the long-lived process instead of
// leaving it for an idle reaper that a sub-idleTimeout cadence will keep
// resetting forever.

// reapTestSession builds a session whose "process" is a real child (a sleeping
// command) so killProcess has something observable to tear down; the fields
// SendLoop touches before the reap decision are seeded.
func reapTestSession(t *testing.T) *WorkspaceSession {
	t.Helper()
	s := newDispatchTestSession(t)
	s.inflight = NewInflightTracker()

	// Fake a live long-lived process: a child that stays alive until killed.
	// stdin is a real pipe because killProcess closes it before signalling.
	cmd := exec.Command("sleep", "30")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Skipf("cannot create stdin pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn test child: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	s.cmd = cmd
	s.stdin = stdin
	s.alive = true
	return s
}

func (s *WorkspaceSession) processAlive() bool {
	s.procMu.Lock()
	defer s.procMu.Unlock()
	return s.alive
}

// TestSchedulerDrivenLoopEndReapsProcess: after a scheduler-driven SendLoop
// finishes its turn with an empty queue and no autohost continue, the
// long-lived process must be reaped (the workspace goes back to zero resident
// agent memory).
func TestSchedulerDrivenLoopEndReapsProcess(t *testing.T) {
	s := reapTestSession(t)

	s.schedulerDriven = true // loop started by DeliverFromScheduler

	// Simulate the loop reaching its terminal decision: clean turn, queue
	// empty, autohost declined (mode != autohost). finalizeSendLoopTurn is the
	// shared end-of-loop sink SendLoop returns through.
	s.finalizeSendLoopTurn(context.Background(), false, "fetched", false)

	if s.processAlive() {
		t.Fatal("scheduler-driven loop ended cleanly but the long-lived process is still alive — it will never be reaped on a <idleTimeout cadence (memory-death regression)")
	}
}

// TestInteractiveLoopEndKeepsProcess: the counter-case — an interactive loop
// (user chat) keeps the process warm for the next message; only the idle
// reaper may claim it. finalizeSendLoopTurn must NOT kill it.
func TestInteractiveLoopEndKeepsProcess(t *testing.T) {
	s := reapTestSession(t)

	s.schedulerDriven = false // loop started by user Deliver / SendKickoff

	s.finalizeSendLoopTurn(context.Background(), false, "answered", false)

	if !s.processAlive() {
		t.Fatal("interactive loop end must keep the process warm — killing here would regress chat latency (process restart per message)")
	}
}

// TestSchedulerLoopDefersReapWhileWorkPending: if finalize defers (paced
// autohost wait / pending future wakeup — the resume will re-drive the loop),
// the process must NOT be reaped yet.
func TestSchedulerLoopDefersReapWhileWorkPending(t *testing.T) {
	s := reapTestSession(t)
	s.schedulerDriven = true
	// A future wakeup: something will re-enter the loop soon.
	s.inflight.AddWakeup("toolu_wake", "poll later", time.Now(), 10*time.Minute)

	s.finalizeSendLoopTurn(context.Background(), false, "wip", false)

	if !s.processAlive() {
		t.Fatal("finalize deferred for a pending wakeup but the process was reaped — the resume would cold-start a new process")
	}
}
