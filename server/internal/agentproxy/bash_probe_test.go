package agentproxy

import (
	"testing"
	"time"
)

func TestInflightTracker_ClearBash(t *testing.T) {
	tr := NewInflightTracker()
	now := time.Now()
	tr.Add(BgTaskBash, "tu_b1", "sleep 600", now)
	tr.Add(BgTaskBash, "tu_b2", "tail -f /var/log", now)
	tr.Add(BgTaskSubagent, "tu_s", "review", now)
	tr.AddWakeup("tu_w", "check build", now, 5*time.Minute)

	removed := tr.ClearBash()
	if len(removed) != 2 {
		t.Fatalf("expected 2 bash entries removed, got %d", len(removed))
	}
	got := tr.Snapshot()
	if len(got) != 2 {
		t.Fatalf("expected 2 remaining entries (subagent + wakeup), got %d", len(got))
	}
	for _, e := range got {
		if e.Kind == BgTaskBash {
			t.Fatalf("ClearBash left a bash entry: %+v", e)
		}
	}
}

func TestInflightTracker_CountByKind(t *testing.T) {
	tr := NewInflightTracker()
	now := time.Now()
	tr.Add(BgTaskBash, "tu_b1", "x", now)
	tr.Add(BgTaskBash, "tu_b2", "y", now)
	tr.Add(BgTaskSubagent, "tu_s", "z", now)
	tr.AddWakeup("tu_w", "w", now, time.Minute)

	if got := tr.CountByKind(BgTaskBash); got != 2 {
		t.Errorf("BgTaskBash count: want 2, got %d", got)
	}
	if got := tr.CountByKind(BgTaskSubagent); got != 1 {
		t.Errorf("BgTaskSubagent count: want 1, got %d", got)
	}
	if got := tr.CountByKind(BgTaskWakeup); got != 1 {
		t.Errorf("BgTaskWakeup count: want 1, got %d", got)
	}
}

func TestCountAliveShellDescendants_InvalidPid(t *testing.T) {
	// Negative / zero PID short-circuits to -1 without hitting the OS.
	if got := countAliveShellDescendantsImpl(0); got != -1 {
		t.Errorf("pid=0: want -1, got %d", got)
	}
	if got := countAliveShellDescendantsImpl(-1); got != -1 {
		t.Errorf("pid=-1: want -1, got %d", got)
	}
}

// withShellProbe swaps countAliveShellDescendantsFn for the duration of fn,
// then restores. Lets gcInflightLoop-style code be tested without spawning
// real shells or relying on gopsutil's OS probe.
func withShellProbe(t *testing.T, stub func(int32) int, fn func()) {
	t.Helper()
	orig := countAliveShellDescendantsFn
	countAliveShellDescendantsFn = stub
	defer func() { countAliveShellDescendantsFn = orig }()
	fn()
}

func TestInflightTracker_TrimBashTo(t *testing.T) {
	now := time.Now()
	// Three bash entries: one without BashID (failed spawn), two with BashIDs
	// at different StartedAt. Plus a subagent and a wakeup that must not be
	// touched by TrimBash.
	tr := NewInflightTracker()
	tr.Add(BgTaskBash, "tu_oldest", "sleep 600", now.Add(-3*time.Minute))
	tr.SetBashID("tu_oldest", "sh_oldest")
	tr.Add(BgTaskBash, "tu_mid", "tail -f", now.Add(-2*time.Minute))
	tr.SetBashID("tu_mid", "sh_mid")
	tr.Add(BgTaskBash, "tu_newest_noack", "ping forever", now.Add(-1*time.Minute))
	// no SetBashID — simulates failed spawn ack
	tr.Add(BgTaskSubagent, "tu_s", "review", now)
	tr.AddWakeup("tu_w", "check", now, 5*time.Minute)

	// Trim down to 2: should drop the no-BashID entry first (priority 1),
	// no second victim needed (we go from 3 → 2 bashes with one removal).
	removed := tr.TrimBashTo(2)
	if len(removed) != 1 {
		t.Fatalf("expected 1 trimmed entry, got %d", len(removed))
	}
	if removed[0].ToolUseID != "tu_newest_noack" {
		t.Fatalf("expected no-BashID victim first, got %q", removed[0].ToolUseID)
	}
	if got := tr.CountByKind(BgTaskBash); got != 2 {
		t.Fatalf("expected 2 bash entries after trim, got %d", got)
	}
	// Subagent + wakeup untouched.
	if got := tr.CountByKind(BgTaskSubagent); got != 1 {
		t.Errorf("subagent count: want 1, got %d", got)
	}
	if got := tr.CountByKind(BgTaskWakeup); got != 1 {
		t.Errorf("wakeup count: want 1, got %d", got)
	}

	// Trim further to 1: oldest by StartedAt should go (tu_oldest).
	removed = tr.TrimBashTo(1)
	if len(removed) != 1 {
		t.Fatalf("expected 1 trimmed entry, got %d", len(removed))
	}
	if removed[0].ToolUseID != "tu_oldest" {
		t.Fatalf("expected oldest victim, got %q", removed[0].ToolUseID)
	}

	// Trim to >= current count is a no-op.
	if got := tr.TrimBashTo(5); got != nil {
		t.Fatalf("expected nil from no-op trim, got %+v", got)
	}
	if got := tr.CountByKind(BgTaskBash); got != 1 {
		t.Fatalf("no-op trim must not change count, got %d", got)
	}

	// Trim to 0 — equivalent to ClearBash for the bash slice.
	removed = tr.TrimBashTo(0)
	if len(removed) != 1 {
		t.Fatalf("expected 1 trimmed entry on trim-to-0, got %d", len(removed))
	}
	if got := tr.CountByKind(BgTaskBash); got != 0 {
		t.Fatalf("expected 0 bash entries after trim-to-0, got %d", got)
	}
	// Subagent / wakeup still intact.
	if got := len(tr.Snapshot()); got != 2 {
		t.Fatalf("expected 2 non-bash entries to survive trim-to-0, got %d", got)
	}
}

func TestInflightTracker_TrimBashTo_NegativeClampedToZero(t *testing.T) {
	tr := NewInflightTracker()
	tr.Add(BgTaskBash, "tu_b", "x", time.Now())
	if got := tr.TrimBashTo(-3); len(got) != 1 {
		t.Fatalf("negative target should clamp to 0 and remove all, got %d", len(got))
	}
	if got := tr.CountByKind(BgTaskBash); got != 0 {
		t.Fatalf("expected 0 bash after trim, got %d", got)
	}
}

func TestShellProbe_StubIsRespected(t *testing.T) {
	// Sanity: the package-level fn variable is actually used (not bypassed
	// somewhere). If a future refactor inlines the impl, this test starts
	// failing immediately rather than silently making the GC probe untestable.
	called := false
	withShellProbe(t, func(pid int32) int {
		called = true
		if pid != 123 {
			t.Errorf("stub received pid %d, want 123", pid)
		}
		return 0
	}, func() {
		if got := countAliveShellDescendantsFn(123); got != 0 {
			t.Errorf("stub return: want 0, got %d", got)
		}
	})
	if !called {
		t.Fatal("stub was not invoked")
	}
}
