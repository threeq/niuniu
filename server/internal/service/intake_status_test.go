package service

import "testing"

func TestDecisionStatus(t *testing.T) {
	cases := []struct {
		decision string
		executed bool
		want     string
	}{
		{"accept", false, "pending"},
		{"resume", false, "pending"},
		{"accept", true, "accepted"},
		{"resume", true, "accepted"},
		{"ignore", false, "ignored"},
		// An ignored decision can never have executed, but the mapping must stay
		// stable if a row is ever written that way.
		{"ignore", true, "ignored"},
	}
	for _, c := range cases {
		if got := decisionStatus(c.decision, c.executed); got != c.want {
			t.Errorf("decisionStatus(%q, %v) = %q, want %q", c.decision, c.executed, got, c.want)
		}
	}
}
