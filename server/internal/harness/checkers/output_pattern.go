package checkers

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/niuniu-dev/niuniu/internal/harness"
)

type outputPatternConfig struct {
	Pattern string `json:"pattern"`
}

// OutputPattern checks whether the agent's accumulated output matches a regex pattern.
// Config: {"pattern": "<regex>"}
type OutputPattern struct{}

func (c *OutputPattern) Name() string { return "output-pattern" }

func (c *OutputPattern) Check(ctx context.Context, opts harness.CheckOpts) harness.CheckResult {
	if opts.AgentOutput == "" {
		return harness.CheckResult{Status: "skip", Message: "no agent output available"}
	}

	var cfg outputPatternConfig
	if opts.SpecConfig != "" {
		json.Unmarshal([]byte(opts.SpecConfig), &cfg)
	}
	if cfg.Pattern == "" {
		return harness.CheckResult{Status: "skip", Message: "no pattern configured"}
	}

	re, err := regexp.Compile(cfg.Pattern)
	if err != nil {
		return harness.CheckResult{
			Status:  "error",
			Message: fmt.Sprintf("invalid pattern: %v", err),
		}
	}

	if re.MatchString(opts.AgentOutput) {
		return harness.CheckResult{
			Status:  "pass",
			Message: fmt.Sprintf("output matches pattern %q", cfg.Pattern),
		}
	}

	return harness.CheckResult{
		Status:  "fail",
		Message: fmt.Sprintf("output does not match pattern %q", cfg.Pattern),
		Details: fmt.Sprintf("pattern: %s\noutput length: %d bytes", cfg.Pattern, len(opts.AgentOutput)),
	}
}
