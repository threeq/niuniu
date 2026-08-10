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
	spec := harness.Spec{
		ID:        raw.ID,
		Scope:     "global", // harness_specs is a global library
		Category:  raw.Category,
		Name:      raw.Name,
		Enabled:   raw.Enabled != 0,
		Severity:  raw.Severity,
		Config:    raw.Config,
	}
	results := e.cr.RunAll(ctx, []harness.Spec{spec}, harness.CheckOpts{
		WorkspacePath: workspacePath,
	})
	if len(results) == 0 {
		// No checker registered — treat as skip (passed).
		return true, "", nil
	}
	r := results[0]
	passed := r.Status == "pass"
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
