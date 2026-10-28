//go:build linux

package sandbox

import (
	"errors"
	"strings"
	"testing"
)

// The seccomp capability signal must reflect what actually happened, not a
// constant. A filter that failed to install must not be reported as available.
func TestSeccompAvailabilityReflectsApplyOutcome(t *testing.T) {
	resetSeccompOutcome := func() {
		seccompOutcomeMu.Lock()
		seccompOutcomeSet, seccompOutcomeErr = false, ""
		seccompOutcomeMu.Unlock()
	}
	resetSeccompOutcome()
	t.Cleanup(resetSeccompOutcome)

	// Unset (as in the server process, which never self-fences): the signal
	// falls back to the build capability rather than a hardcoded true.
	if !seccompAvailable() {
		t.Fatal("seccomp is not buildable on a supported linux architecture")
	}

	// A failed install must flip the signal to false and name the failure.
	recordSeccompOutcome(errors.New("prctl(PR_SET_SECCOMP): EPERM"))
	if seccompAvailable() {
		t.Fatal("a failed seccomp install was still reported as available")
	}
	if d := seccompDetail(); !strings.Contains(d, "did NOT install") {
		t.Fatalf("the detail does not report the failed install: %q", d)
	}

	// A successful install flips it back, and the detail distinguishes
	// "applied" from "verified" instead of over-claiming containment.
	recordSeccompOutcome(nil)
	if !seccompAvailable() {
		t.Fatal("a successful seccomp install was reported as unavailable")
	}
	if d := seccompDetail(); !strings.Contains(d, "self-test") {
		t.Fatalf("the detail does not distinguish applied from verified: %q", d)
	}
}
