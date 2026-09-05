package service

import (
	"context"
	"fmt"

	"github.com/niuniu-dev/niuniu/internal/harness"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// GateSpecExecutor runs a single spec against a workspace and returns pass/fail + log.
// Implemented by an adapter over existing harness.CheckRunner.
type GateSpecExecutor interface {
	ExecuteSpec(ctx context.Context, runID, specID int64, workspacePath string) (passed bool, output string, err error)
}

// GateFailure records one failed spec execution within a gate run.
type GateFailure struct {
	SpecID int64  `json:"specId"`
	Output string `json:"output"` // truncated to 4KB
	Reason string `json:"reason"` // "exit_nonzero" | "timeout" | "executor_error"
}

// checkRunnerExec adapts harness.CheckRunner to the GateSpecExecutor interface.
// It fetches the spec by ID, converts it to the harness.Spec format, and delegates
// to CheckRunner.RunAll for a single-spec execution.
type checkRunnerExec struct {
	cr *harness.CheckRunner
	q  *store.Queries
}

// NewCheckRunnerExec creates a GateSpecExecutor backed by the existing harness
// CheckRunner infrastructure. The CheckRunner must have checkers registered for
// all spec category/name keys that column gate specs reference.
func NewCheckRunnerExec(cr *harness.CheckRunner, q *store.Queries) GateSpecExecutor {
	return &checkRunnerExec{cr: cr, q: q}
}

// ExecuteSpec fetches specID from the store, runs it via CheckRunner.RunAll, and
// returns (passed, output, error). Treats no registered checker as a skip (passed).
func (e *checkRunnerExec) ExecuteSpec(ctx context.Context, runID, specID int64, workspacePath string) (bool, string, error) {
	if e.cr == nil || e.q == nil {
		// No CheckRunner available (test stub path or not yet wired).
		return true, "", nil
	}
	raw, err := e.q.GetHarnessSpec(ctx, specID)
	if err != nil {
		return false, "", fmt.Errorf("get spec %d: %w", specID, err)
	}
	// Convert via storeSpecsToHarness so ALL typed columns (Kind, Command,
	// Pattern, Target, TimeoutSec, threshold, judge_*, ...) reach the checker.
	// Hand-copying only the legacy subset here dropped Kind, which made
	// CheckRunner.dispatch fall back to the legacy category/name registry: a
	// UI-configured spec (typed columns populated, Config left "{}") then read
	// its command out of the empty Config and returned skip — or, for
	// quality/build-test-pass which has no legacy checker at all, produced no
	// result and passed unconditionally. Every column/floor/exit gate was
	// silently vacuous.
	specs := storeSpecsToHarness([]store.HarnessSpec{raw})
	results := e.cr.RunAll(ctx, specs, harness.CheckOpts{
		WorkspacePath: workspacePath,
	})
	if len(results) == 0 {
		// No checker registered — treat as skip (passed).
		return true, "", nil
	}
	r := results[0]
	// Only an explicit "fail" blocks. A "skip" means the spec is not
	// configured enough to judge anything (e.g. an empty command) — treating
	// that as a failure would false-block completion on the shipped-disabled
	// defaults, which are documented as never false-blocking.
	passed := r.Status != "fail"
	output := r.Message
	if r.Details != "" {
		output += "\n" + r.Details
	}
	return passed, output, nil
}

// truncateStr returns s truncated to maxLen bytes. Used by the column-native
// exit / floor gates to cap captured spec output.
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
