package agentproxy

import (
	"sync"
	"testing"
	"time"
)

func TestInflightTracker_AddRemove(t *testing.T) {
	tr := NewInflightTracker()
	tr.Add(BgTaskBash, "tu_1", "echo hi", time.Now())
	if got := tr.Snapshot(); len(got) != 1 || got[0].Kind != BgTaskBash || got[0].Title != "echo hi" {
		t.Fatalf("expected one bash task, got %+v", got)
	}
	tr.Remove("tu_1")
	if got := tr.Snapshot(); len(got) != 0 {
		t.Fatalf("expected empty, got %+v", got)
	}
}

func TestInflightTracker_AddWakeup(t *testing.T) {
	tr := NewInflightTracker()
	now := time.Date(2026, 4, 27, 10, 0, 0, 0, time.UTC)
	tr.AddWakeup("tu_w", "checking build", now, 300*time.Second)
	got := tr.Snapshot()
	if len(got) != 1 || got[0].Kind != BgTaskWakeup {
		t.Fatalf("expected one wakeup, got %+v", got)
	}
	if got[0].ScheduledFor.Sub(now) != 300*time.Second {
		t.Fatalf("expected ScheduledFor = now+300s, got %v", got[0].ScheduledFor)
	}
}

func TestInflightTracker_DuplicateAdd(t *testing.T) {
	tr := NewInflightTracker()
	tr.Add(BgTaskBash, "tu_1", "first", time.Now())
	tr.Add(BgTaskBash, "tu_1", "second", time.Now())
	got := tr.Snapshot()
	if len(got) != 1 || got[0].Title != "second" {
		t.Fatalf("expected second add to overwrite, got %+v", got)
	}
}

func TestInflightTracker_RemoveMissing(t *testing.T) {
	tr := NewInflightTracker()
	if got := tr.Remove("never_added"); got {
		t.Fatal("expected Remove to return false for missing id")
	}
	if len(tr.Snapshot()) != 0 {
		t.Fatal("expected empty tracker")
	}
}

func TestInflightTracker_RemoveReturnsTrueOnHit(t *testing.T) {
	tr := NewInflightTracker()
	tr.Add(BgTaskBash, "tu_1", "x", time.Now())
	if got := tr.Remove("tu_1"); !got {
		t.Fatal("expected Remove to return true for present id")
	}
	if got := tr.Remove("tu_1"); got {
		t.Fatal("expected second Remove to return false")
	}
}

func TestInflightTracker_GCStale(t *testing.T) {
	tr := NewInflightTracker()
	now := time.Date(2026, 4, 27, 10, 0, 0, 0, time.UTC)
	tr.Add(BgTaskBash, "old_bash", "long-running", now.Add(-2*time.Hour))     // 2h old → GC
	tr.Add(BgTaskBash, "new_bash", "fresh", now.Add(-5*time.Minute))           // recent → keep
	tr.Add(BgTaskSubagent, "old_sub", "long-sub", now.Add(-45*time.Minute))    // 45m old → GC
	tr.AddWakeup("expired_wake", "wake reason", now.Add(-1*time.Hour), 30*time.Minute) // ScheduledFor = now-30m → GC
	tr.AddWakeup("future_wake", "wake reason", now, 10*time.Minute)            // ScheduledFor = now+10m → keep

	removed := tr.GCStale(now)
	if len(removed) != 3 {
		t.Fatalf("expected 3 removed, got %d: %+v", len(removed), removed)
	}
	if got := tr.Snapshot(); len(got) != 2 {
		t.Fatalf("expected 2 remaining, got %d", len(got))
	}
}

func TestInflightTracker_Concurrent(t *testing.T) {
	tr := NewInflightTracker()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			tr.Add(BgTaskBash, fmtKey(i), "x", time.Now())
		}(i)
		go func(i int) {
			defer wg.Done()
			_ = tr.Snapshot()
		}(i)
	}
	wg.Wait()
	if got := len(tr.Snapshot()); got != 100 {
		t.Fatalf("expected 100, got %d", got)
	}
}

func fmtKey(i int) string {
	return "tu_" + string(rune('0'+i%10)) + string(rune('0'+i/10))
}

func TestWorkspaceSession_BgTasks_ReflectsTracker(t *testing.T) {
	s := &WorkspaceSession{inflight: NewInflightTracker()}
	if got := s.BgTasks(); len(got) != 0 {
		t.Fatalf("expected empty, got %+v", got)
	}
	s.inflight.Add(BgTaskBash, "tu", "echo", time.Now())
	if got := s.BgTasks(); len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
}

func TestWorkspaceSession_BgTasks_NilSafe(t *testing.T) {
	s := &WorkspaceSession{}
	if got := s.BgTasks(); got != nil {
		t.Fatalf("expected nil for unset tracker, got %+v", got)
	}
}

func TestInflightTracker_Get(t *testing.T) {
	tr := NewInflightTracker()
	if _, ok := tr.Get("missing"); ok {
		t.Fatal("expected ok=false for missing id")
	}
	tr.Add(BgTaskSubagent, "tu_s", "review", time.Now())
	got, ok := tr.Get("tu_s")
	if !ok {
		t.Fatal("expected ok=true after Add")
	}
	if got.Kind != BgTaskSubagent || got.Title != "review" {
		t.Fatalf("unexpected entry: %+v", got)
	}
}

func TestInflightTracker_SetBashID(t *testing.T) {
	tr := NewInflightTracker()
	if ok := tr.SetBashID("tu_b", "shell1"); ok {
		t.Fatal("expected false when entry missing")
	}
	tr.Add(BgTaskBash, "tu_b", "sleep", time.Now())
	if ok := tr.SetBashID("tu_b", "shell1"); !ok {
		t.Fatal("expected true on first set")
	}
	got, _ := tr.Get("tu_b")
	if got.BashID != "shell1" {
		t.Fatalf("expected BashID=shell1, got %q", got.BashID)
	}
	if ok := tr.SetBashID("tu_b", "shell1"); ok {
		t.Fatal("expected false on duplicate set with same value")
	}
	// SetBashID rejects non-bash kinds
	tr.Add(BgTaskSubagent, "tu_s", "x", time.Now())
	if ok := tr.SetBashID("tu_s", "shell2"); ok {
		t.Fatal("expected false for non-bash kind")
	}
}

func TestInflightTracker_RemoveByBashID(t *testing.T) {
	tr := NewInflightTracker()
	tr.Add(BgTaskBash, "tu_b1", "first", time.Now())
	tr.SetBashID("tu_b1", "shell_a")
	tr.Add(BgTaskBash, "tu_b2", "second", time.Now())
	tr.SetBashID("tu_b2", "shell_b")

	if ok := tr.RemoveByBashID(""); ok {
		t.Fatal("expected false for empty bashID")
	}
	if ok := tr.RemoveByBashID("nonexistent"); ok {
		t.Fatal("expected false for unknown bashID")
	}
	if ok := tr.RemoveByBashID("shell_a"); !ok {
		t.Fatal("expected true when match found")
	}
	got := tr.Snapshot()
	if len(got) != 1 || got[0].ToolUseID != "tu_b2" {
		t.Fatalf("expected only tu_b2 remaining, got %+v", got)
	}
}
