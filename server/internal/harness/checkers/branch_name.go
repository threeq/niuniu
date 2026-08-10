package checkers

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/niuniu-dev/niuniu/internal/harness"
)

const defaultBranchPattern = `^(feat|fix|refactor|chore|docs|test|perf|ci|ws-\d+)/[a-z0-9][a-z0-9\-]*$`

var protectedBranches = map[string]bool{
	"main":    true,
	"master":  true,
	"develop": true,
	"dev":     true,
	"staging": true,
	"release": true,
}

type branchNameConfig struct {
	Pattern string `json:"pattern"`
}

type BranchName struct{}

func NewBranchName() *BranchName {
	return &BranchName{}
}

func (b *BranchName) Name() string { return "branch-name" }

func (b *BranchName) Check(ctx context.Context, opts harness.CheckOpts) harness.CheckResult {
	start := time.Now()

	if opts.BranchName == "" {
		return harness.CheckResult{
			Status:     "skip",
			Message:    "no branch name provided",
			DurationMs: time.Since(start).Milliseconds(),
		}
	}

	if protectedBranches[opts.BranchName] {
		return harness.CheckResult{
			Status:     "pass",
			Message:    fmt.Sprintf("branch %q is a protected branch", opts.BranchName),
			DurationMs: time.Since(start).Milliseconds(),
		}
	}

	pattern := defaultBranchPattern
	if opts.SpecConfig != "" {
		var cfg branchNameConfig
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

	if re.MatchString(opts.BranchName) {
		return harness.CheckResult{
			Status:     "pass",
			Message:    "branch name matches naming convention",
			DurationMs: time.Since(start).Milliseconds(),
		}
	}

	return harness.CheckResult{
		Status:     "fail",
		Message:    fmt.Sprintf("branch %q does not match naming convention", opts.BranchName),
		Details:    fmt.Sprintf("branch: %q\nexpected pattern: %s", opts.BranchName, pattern),
		DurationMs: time.Since(start).Milliseconds(),
	}
}
