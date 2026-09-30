package loop

import (
	"strings"
	"testing"
)

// Dead ends (tried-and-failed approaches) must accumulate across chained
// compactions and render as an explicit do-not-retry list — without them the
// continuation model re-derives and re-fails the same paths.
func TestMergeState_DeadEndsAccumulate(t *testing.T) {
	old := &CompactState{
		Goal:     "fix the build",
		DeadEnds: []string{"rm -rf node_modules — build is not node-based"},
	}
	nw := &CompactState{
		Goal:         "fix the build",
		KeyDecisions: []string{"build via make"},
		DeadEnds:     []string{"bumping go.mod version — toolchain pin overrides it"},
	}
	got := mergeState(old, nw)
	if len(got.DeadEnds) != 2 {
		t.Fatalf("dead_ends = %v, want both entries accumulated", got.DeadEnds)
	}
	if got.DeadEnds[0] != "rm -rf node_modules — build is not node-based" {
		t.Errorf("oldest dead end not preserved first: %v", got.DeadEnds)
	}
}

func TestMergeState_DeadEndsCapped(t *testing.T) {
	old := &CompactState{}
	nw := &CompactState{}
	for i := 0; i < maxDeadEnds+5; i++ {
		nw.DeadEnds = append(nw.DeadEnds, "dead end")
	}
	got := mergeState(old, nw)
	if len(got.DeadEnds) > maxDeadEnds {
		t.Fatalf("dead_ends = %d, want <= %d", len(got.DeadEnds), maxDeadEnds)
	}
}

func TestRenderState_DeadEndsLabeled(t *testing.T) {
	out := renderState(&CompactState{
		Goal:     "g",
		DeadEnds: []string{"approach A failed: reason"},
	})
	if !strings.Contains(out, "do NOT retry") {
		t.Errorf("render must label dead ends as do-not-retry, got:\n%s", out)
	}
	if !strings.Contains(out, "approach A failed: reason") {
		t.Errorf("render lost the dead end entry:\n%s", out)
	}
}

// The summarizer contract must advertise the dead_ends field, and the parser
// must accept states that carry it.
func TestCompactSystem_AdvertisesDeadEnds(t *testing.T) {
	if !strings.Contains(compactSystem, "dead_ends") {
		t.Error("compactSystem does not document dead_ends — the summarizer will never emit it")
	}
	if _, err := parseStateJSON(`{"goal":"g","key_decisions":["k"],"open_items":["o"],"dead_ends":["x"]}`); err != nil {
		t.Errorf("parseStateJSON rejects dead_ends: %v", err)
	}
}
