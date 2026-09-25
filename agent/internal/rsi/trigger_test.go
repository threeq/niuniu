package rsi

import (
	"strings"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/eval"
)

func names(ss ...string) []string { return ss }

func TestShouldTriggerDecisionTable(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name         string
		failures     []string
		lastFailures []string
		lastExplore  time.Time
		cooldown     time.Duration
		want         bool
	}{
		{"all green → never", nil, names("a"), now.Add(-time.Hour), time.Hour, false},
		{"fresh failure, no history → yes", names("a"), nil, time.Time{}, 0, true},
		{"repeat failure outside cooldown → yes", names("a", "b"), names("a"), now.Add(-48 * time.Hour), time.Hour, true},
		{"repeat failure within cooldown → no", names("a"), names("a"), now.Add(-time.Hour), 24 * time.Hour, false},
		{"new task failure (no overlap) → no", names("c"), names("a"), now.Add(-48 * time.Hour), time.Hour, false},
		{"partial overlap → yes", names("a", "c"), names("a", "b"), now.Add(-48 * time.Hour), time.Hour, true},
		{"cooldown zero → default 24h applies", names("a"), names("a"), now.Add(-2 * time.Hour), 0, false},
	}
	for _, tc := range cases {
		if got := ShouldTrigger(tc.failures, tc.lastFailures, tc.lastExplore, tc.cooldown); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestShouldTriggerSummary(t *testing.T) {
	now := time.Now()
	mkSummary := func(failures ...string) *eval.Summary {
		s := &eval.Summary{GeneratedAt: now}
		for _, f := range failures {
			s.Results = append(s.Results, eval.Result{Name: f, Pass: false, Error: "x"})
		}
		s.Results = append(s.Results, eval.Result{Name: "ok-task", Pass: true})
		return s
	}
	cur := mkSummary("14-append-changelog")
	prev := mkSummary("14-append-changelog")

	if !ShouldTriggerSummary(cur, prev, now.Add(-48*time.Hour), time.Hour) {
		t.Error("repeated failure outside cooldown should trigger")
	}
	if ShouldTriggerSummary(cur, prev, now.Add(-time.Hour), 24*time.Hour) {
		t.Error("within cooldown should not trigger")
	}
	if ShouldTriggerSummary(mkSummary(), prev, now.Add(-48*time.Hour), time.Hour) {
		t.Error("all-green should not trigger")
	}
	if ShouldTriggerSummary(nil, nil, time.Now(), time.Hour) {
		t.Error("nil summary should not trigger")
	}
	// 大小写不敏感重叠。
	curCS := mkSummary("Fix-Case")
	prevCS := mkSummary("fix-case")
	if !ShouldTriggerSummary(curCS, prevCS, now.Add(-48*time.Hour), time.Hour) {
		t.Error("case-insensitive overlap missed")
	}
}

func TestOverlap(t *testing.T) {
	got := overlap(names("A", "b", "c"), names("b", "D"))
	if len(got) != 1 || got[0] != "b" {
		t.Errorf("overlap = %v", got)
	}
	if len(overlap(nil, names("x"))) != 0 {
		t.Error("empty side must yield empty overlap")
	}
	_ = strings.TrimSpace
}
