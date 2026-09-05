package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/harness"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// defaultFloorCommandTimeoutSec bounds a project 底线 command that was stored
// without an explicit timeout. Generous because a floor is typically a full
// build+test run.
const defaultFloorCommandTimeoutSec = 600

// GateSpecExecutor runs a gate check against a workspace and returns pass/fail + log.
// Implemented by an adapter over existing harness.CheckRunner.
type GateSpecExecutor interface {
	ExecuteSpec(ctx context.Context, runID, specID int64, workspacePath string) (passed bool, output string, err error)
	// ExecuteCommand runs a bare shell command as a gate check, for the project's
	// 底线 command (projects.floor_command) which is a plain column rather than a
	// harness_specs row. It reuses the same checker so timeout / exit-code / output
	// capture semantics are identical to a command_exit_code spec.
	ExecuteCommand(ctx context.Context, command string, timeoutSec int, workspacePath string) (passed bool, output string, err error)
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
// CheckRunner infrastructure. The CheckRunner must have a TypedChecker registered
// for every Kind that column gate specs reference.
func NewCheckRunnerExec(cr *harness.CheckRunner, q *store.Queries) GateSpecExecutor {
	return &checkRunnerExec{cr: cr, q: q}
}

// ExecuteCommand runs command as a synthetic command_exit_code spec (ID 0, not
// persisted). A blank command passes: an unconfigured floor must never block.
func (e *checkRunnerExec) ExecuteCommand(ctx context.Context, command string, timeoutSec int, workspacePath string) (bool, string, error) {
	if strings.TrimSpace(command) == "" {
		return true, "", nil
	}
	if e.cr == nil {
		return true, "", nil
	}
	if timeoutSec <= 0 {
		timeoutSec = defaultFloorCommandTimeoutSec
	}
	res := e.cr.RunSingle(ctx, harness.Spec{
		Category:   "quality",
		Name:       "floor-command",
		Enabled:    true,
		Severity:   "error",
		Kind:       harness.KindCommandExitCode,
		Command:    command,
		TimeoutSec: timeoutSec,
	}, harness.CheckEnv{WorkspacePath: workspacePath})

	output := res.Message
	if res.Details != "" {
		output += "\n" + res.Details
	}
	return res.Status != "fail" && res.Status != "error", output, nil
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
		// RunAll only drops disabled specs, and the caller already filtered on
		// enabled=1. Reaching here means nothing ran, which is not evidence the
		// standard was met — report it rather than passing quietly.
		return false, "", fmt.Errorf("spec %d produced no result", specID)
	}
	r := results[0]
	// "fail" (checker judged it failing) and "error" (checker could not run,
	// e.g. unknown kind) both block. "skip" does not: it means the spec is not
	// configured enough to judge anything — an empty command, say — and the
	// shipped defaults are documented as never false-blocking until configured.
	passed := r.Status != "fail" && r.Status != "error"
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
