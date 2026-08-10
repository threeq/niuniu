package checkers

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/niuniu-dev/niuniu/internal/harness"
)

const defaultCommitPattern = `^(feat|fix|refactor|docs|test|chore|perf|ci)(\(.+\))?: .+`

type commitLintConfig struct {
	Pattern string `json:"pattern"`
}

type CommitLint struct{}

func NewCommitLint() *CommitLint {
	return &CommitLint{}
}

func (c *CommitLint) Name() string { return "commit-lint" }

func (c *CommitLint) Check(ctx context.Context, opts harness.CheckOpts) harness.CheckResult {
	start := time.Now()

	if opts.CommitMessage == "" {
		return harness.CheckResult{
			Status:     "skip",
			Message:    "no commit message provided",
			DurationMs: time.Since(start).Milliseconds(),
		}
	}

	pattern := defaultCommitPattern
	if opts.SpecConfig != "" {
		var cfg commitLintConfig
		if err := json.Unmarshal([]byte(opts.SpecConfig), &cfg); err == nil && cfg.Pattern != "" {
			pattern = cfg.Pattern
		}
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return harness.CheckResult{
			Status:     "error",
			Message:    fmt.Sprintf("invalid pattern: %v", err),
			DurationMs: time.Since(start).Milliseconds(),
		}
	}

	if re.MatchString(opts.CommitMessage) {
		return harness.CheckResult{
			Status:     "pass",
			Message:    "commit message matches conventional commit format",
			DurationMs: time.Since(start).Milliseconds(),
		}
	}

	return harness.CheckResult{
		Status:     "fail",
		Message:    "commit message does not match conventional commit format",
		Details:    fmt.Sprintf("message: %q\nexpected pattern: %s", opts.CommitMessage, pattern),
		DurationMs: time.Since(start).Milliseconds(),
	}
}

// truncate returns s truncated to at most max bytes, appending "..." if truncated.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
