package service

import "testing"

func TestDecisionStatus(t *testing.T) {
	cases := []struct {
		decision string
		executed bool
		consumed bool
		want     string
	}{
		{"accept", false, false, "pending"},
		{"resume", false, false, "pending"},
		{"accept", true, false, "accepted"},
		{"resume", true, false, "accepted"},
		{"accept", false, true, "consumed"},
		{"ignore", false, false, "ignored"},
		{"ignore", true, false, "ignored"},
	}
	for _, c := range cases {
		if got := decisionStatus(c.decision, c.executed, c.consumed); got != c.want {
			t.Errorf("decisionStatus(%q, %v, %v) = %q, want %q", c.decision, c.executed, c.consumed, got, c.want)
		}
	}
}
