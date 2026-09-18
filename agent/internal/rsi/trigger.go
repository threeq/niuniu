package rsi

import (
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/eval"
)

// Auto-trigger policy: RSI is NOT enabled by default (measured on the 20-task
// general corpus it showed no gain — see README). When opted in
// (`niuniu-agent eval -auto-explore`), it fires only on a REPEATED failure
// pattern: the current eval run failed at least one task AND overlaps the
// previous run's failures — a one-off flake must not burn explore cycles —
// and outside the cooldown window.

// DefaultTriggerCooldown is the minimum interval between auto-triggered
// explore runs.
const DefaultTriggerCooldown = 24 * time.Hour

// TriggerState is the persisted trigger bookkeeping (small JSON next to the
// eval reports).
type TriggerState struct {
	LastExplore  time.Time `json:"last_explore"`
	LastFailures []string  `json:"last_failures,omitempty"`
}

// ShouldTrigger decides whether an auto-explore run is warranted.
//
//	failNow == false            → never (all green, nothing to improve)
//	overlap == empty (new task) → no (first failure: wait for repetition)
//	overlap != empty,
//	within cooldown             → no (already addressed recently)
//	overlap != empty,
//	outside cooldown            → yes (repeated failure pattern)
func ShouldTrigger(failures, lastFailures []string, lastExplore time.Time, cooldown time.Duration) bool {
	if len(failures) == 0 {
		return false
	}
	if lastExplore.IsZero() {
		return true // first opt-in run: no history, treat fresh failure as signal
	}
	if cooldown <= 0 {
		cooldown = DefaultTriggerCooldown
	}
	if time.Since(lastExplore) < cooldown {
		return false
	}
	return len(overlap(failures, lastFailures)) > 0
}

// overlap returns the intersection of two failure-name sets.
func overlap(a, b []string) []string {
	set := make(map[string]bool, len(b))
	for _, n := range b {
		set[strings.ToLower(strings.TrimSpace(n))] = true
	}
	var out []string
	for _, n := range a {
		if set[strings.ToLower(strings.TrimSpace(n))] {
			out = append(out, n)
		}
	}
	return out
}

// ShouldTriggerSummary adapts ShouldTrigger to an eval Summary pair.
func ShouldTriggerSummary(cur, prev *eval.Summary, lastExplore time.Time, cooldown time.Duration) bool {
	if cur == nil {
		return false
	}
	if prev != nil && prev.GeneratedAt.After(lastExplore) && !lastExplore.IsZero() {
		// prev run is newer than the last explore — its failures ARE the
		// "last failures" baseline.
		return ShouldTrigger(failedNames(cur), failedNames(prev), lastExplore, cooldown)
	}
	return ShouldTrigger(failedNames(cur), nil, lastExplore, cooldown)
}

func failedNames(s *eval.Summary) []string {
	if s == nil {
		return nil
	}
	var out []string
	for _, r := range s.Results {
		if !r.Pass {
			out = append(out, r.Name)
		}
	}
	return out
}
