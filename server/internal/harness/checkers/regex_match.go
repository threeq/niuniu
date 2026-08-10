package checkers

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/niuniu-dev/niuniu/internal/harness"
)

// RegexMatch is a generic typed checker that evaluates a regex pattern
// against a configurable input target (commit message, branch name,
// agent output).
type RegexMatch struct{}

// NewRegexMatch returns a new RegexMatch checker.
func NewRegexMatch() *RegexMatch { return &RegexMatch{} }

// Kind reports the registered Kind for this checker.
func (c *RegexMatch) Kind() string { return harness.KindRegexMatch }

// Run executes the regex match against the input selected by spec.Target.
func (c *RegexMatch) Run(ctx context.Context, spec harness.Spec, env harness.CheckEnv) harness.CheckResult {
	start := time.Now()
	if spec.Pattern == "" {
		return harness.CheckResult{
			Status:     "skip",
			Message:    "no pattern configured",
			DurationMs: time.Since(start).Milliseconds(),
		}
	}
	target := pickRegexTarget(spec.Target, env)
	if target == "" {
		return harness.CheckResult{
			Status:     "skip",
			Message:    fmt.Sprintf("no input for target %q", spec.Target),
			DurationMs: time.Since(start).Milliseconds(),
		}
	}
	pattern := spec.Pattern
	if spec.PatternFlags != "" {
		pattern = "(?" + spec.PatternFlags + ")" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return harness.CheckResult{
			Status:     "error",
			Message:    fmt.Sprintf("invalid pattern: %v", err),
			DurationMs: time.Since(start).Milliseconds(),
		}
	}
	if re.MatchString(target) {
		return harness.CheckResult{
			Status:     "pass",
			Message:    "matches pattern",
			DurationMs: time.Since(start).Milliseconds(),
		}
	}
	return harness.CheckResult{
		Status:     "fail",
		Message:    "does not match pattern",
		Details:    fmt.Sprintf("input: %q\npattern: %s", truncate(target, 200), pattern),
		DurationMs: time.Since(start).Milliseconds(),
	}
}

// pickRegexTarget selects the runtime input string based on spec.Target.
func pickRegexTarget(target string, env harness.CheckEnv) string {
	switch target {
	case harness.TargetCommitMessage:
		return env.CommitMessage
	case harness.TargetBranchName:
		return env.BranchName
	case harness.TargetAgentOutput:
		return env.AgentOutput
	default:
		return ""
	}
}
