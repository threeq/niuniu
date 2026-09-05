package harness

import (
	"context"
	"log/slog"
	"time"
)

// CheckRunner dispatches checks for harness specs. Every checker is a
// TypedChecker registered under its Kind(); dispatch is a single lookup on
// Spec.Kind.
type CheckRunner struct {
	typedCheckers map[string]TypedChecker
}

// NewCheckRunner creates an empty runner.
func NewCheckRunner() *CheckRunner {
	return &CheckRunner{
		typedCheckers: make(map[string]TypedChecker),
	}
}

// RegisterTyped adds a TypedChecker under its Kind().
func (r *CheckRunner) RegisterTyped(c TypedChecker) {
	r.typedCheckers[c.Kind()] = c
}

// RunSingle executes one spec synchronously and returns the result. Used by
// the on-demand API endpoint.
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

// dispatch routes the spec to the TypedChecker registered under its Kind.
// An unknown Kind is an "error", not a "skip": a spec that cannot execute must
// never read as a quiet pass, or a gate built on it silently lets everything
// through. Caller sets SpecID + DurationMs.
func (r *CheckRunner) dispatch(ctx context.Context, spec Spec, env CheckEnv) CheckResult {
	tc, ok := r.typedCheckers[spec.Kind]
	if !ok {
		slog.Error("harness: no checker registered for kind",
			"kind", spec.Kind, "spec", SpecKey(spec))
		return CheckResult{
			Status:  "error",
			Message: "no checker registered for kind " + spec.Kind,
		}
	}
	return tc.Run(ctx, spec, env)
}

// RunAll executes every enabled spec. Disabled specs are skipped; a spec whose
// Kind has no checker yields an "error" result so the misconfiguration surfaces
// rather than passing quietly.
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

// HasBlockingFailure returns true if any error-severity spec failed or could not
// be executed. Both "fail" and "error" block: an unexecutable error-severity spec
// is not evidence that the standard was met.
func (r *CheckRunner) HasBlockingFailure(specs []Spec, results []CheckResult) bool {
	severityByID := make(map[int64]string, len(specs))
	for _, s := range specs {
		severityByID[s.ID] = s.Severity
	}
	for _, res := range results {
		if severityByID[res.SpecID] != "error" {
			continue
		}
		if res.Status == "fail" || res.Status == "error" {
			return true
		}
	}
	return false
}
