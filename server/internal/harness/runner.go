package harness

import (
	"context"
	"log/slog"
	"time"
)

// CheckRunner dispatches checks for harness specs. It supports two registries:
//
//	typedCheckers — keyed by Kind (new, post-2026-05-20). Each TypedChecker
//	reads typed Spec fields directly.
//	checkers      — legacy registry keyed by category/name. Retained for
//	                rows that have not been migrated to a known Kind.
//
// Dispatch prefers typedCheckers[Kind] when available; falls back to the
// legacy lookup otherwise.
type CheckRunner struct {
	checkers      map[string]Checker
	typedCheckers map[string]TypedChecker
}

// NewCheckRunner creates an empty runner.
func NewCheckRunner() *CheckRunner {
	return &CheckRunner{
		checkers:      make(map[string]Checker),
		typedCheckers: make(map[string]TypedChecker),
	}
}

// Register adds a legacy checker under the given key (typically category/name).
// Deprecated: prefer RegisterTyped.
func (r *CheckRunner) Register(key string, c Checker) {
	r.checkers[key] = c
}

// RegisterTyped adds a TypedChecker under its Kind().
func (r *CheckRunner) RegisterTyped(c TypedChecker) {
	r.typedCheckers[c.Kind()] = c
}

// RunSingle executes one spec synchronously and returns the result. Used by
// the on-demand API endpoint. Returns a skip result when no checker matches.
func (r *CheckRunner) RunSingle(ctx context.Context, spec Spec, env CheckEnv) CheckResult {
	if !spec.Enabled {
		return CheckResult{SpecID: spec.ID, Status: "skip", Message: "spec disabled"}
	}
	start := time.Now()
	res := r.dispatch(ctx, spec, env)
	res.SpecID = spec.ID
	if res.DurationMs == 0 {
		res.DurationMs = time.Since(start).Milliseconds()
	}
	return res
}

// dispatch picks a typed checker by Kind, falling back to legacy by SpecKey.
// Caller is responsible for setting SpecID + DurationMs.
func (r *CheckRunner) dispatch(ctx context.Context, spec Spec, env CheckEnv) CheckResult {
	if spec.Kind != "" {
		if tc, ok := r.typedCheckers[spec.Kind]; ok {
			return tc.Run(ctx, spec, env)
		}
	}
	key := SpecKey(spec)
	legacy, ok := r.checkers[key]
	if !ok {
		slog.Warn("harness: no checker registered for spec",
			"kind", spec.Kind, "spec", key)
		return CheckResult{Status: "skip", Message: "no checker for this spec"}
	}
	opts := CheckOpts{
		WorkspacePath: env.WorkspacePath,
		WorktreePaths: env.WorktreePaths,
		CommitMessage: env.CommitMessage,
		BranchName:    env.BranchName,
		AgentOutput:   env.AgentOutput,
		Phase:         env.Phase,
		SpecConfig:    spec.Config,
	}
	return legacy.Check(ctx, opts)
}

// RunAll executes all enabled specs that have a registered checker (typed or
// legacy). Disabled specs and specs without any matching checker are skipped.
func (r *CheckRunner) RunAll(ctx context.Context, specs []Spec, opts CheckOpts) []CheckResult {
	slog.Info("harness: running gate checks", "specCount", len(specs), "workspacePath", opts.WorkspacePath)
	env := CheckOptsToEnv(opts)
	results := make([]CheckResult, 0, len(specs))

	for _, s := range specs {
		if !s.Enabled {
			slog.Debug("harness: skipping disabled spec", "spec", SpecKey(s))
			continue
		}
		start := time.Now()
		res := r.dispatch(ctx, s, env)
		// dispatch returns skip for "no checker" — only surface real results
		if res.Status == "skip" && res.Message == "no checker for this spec" {
			continue
		}
		res.SpecID = s.ID
		if res.DurationMs == 0 {
			res.DurationMs = time.Since(start).Milliseconds()
		}
		slog.Info("harness: check result",
			"spec", SpecKey(s), "kind", s.Kind,
			"status", res.Status, "durationMs", res.DurationMs, "message", res.Message)
		results = append(results, res)
	}

	passed, failed := 0, 0
	for _, res := range results {
		switch res.Status {
		case "pass":
			passed++
		case "fail":
			failed++
		}
	}
	slog.Info("harness: gate checks complete", "total", len(results), "passed", passed, "failed", failed)
	return results
}

// HasBlockingFailure returns true if any result with error-severity spec has a failed status.
func (r *CheckRunner) HasBlockingFailure(specs []Spec, results []CheckResult) bool {
	severityByID := make(map[int64]string, len(specs))
	for _, s := range specs {
		severityByID[s.ID] = s.Severity
	}
	for _, res := range results {
		if res.Status == "fail" && severityByID[res.SpecID] == "error" {
			return true
		}
	}
	return false
}
