package checkers

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"time"

	"github.com/niuniu-dev/niuniu/internal/harness"
)

type commandOutputConfig struct {
	Command    string `json:"command"`
	Pattern    string `json:"pattern"`
	TimeoutSec int    `json:"timeout_sec"`
}

// CommandOutput runs a shell command and checks the output matches a regex pattern.
// Config: {"command": "go test -cover ./...", "pattern": "coverage:\\s*\\d+", "timeout_sec": 120}
type CommandOutput struct{}

func (c *CommandOutput) Name() string { return "command-output" }

func (c *CommandOutput) Check(ctx context.Context, opts harness.CheckOpts) harness.CheckResult {
	var cfg commandOutputConfig
	if opts.SpecConfig != "" {
		json.Unmarshal([]byte(opts.SpecConfig), &cfg)
	}
	if cfg.Command == "" || cfg.Pattern == "" {
		return harness.CheckResult{Status: "skip", Message: "command and pattern are both required"}
	}
	if cfg.TimeoutSec <= 0 {
		cfg.TimeoutSec = 120
	}

	re, err := regexp.Compile(cfg.Pattern)
	if err != nil {
		return harness.CheckResult{
			Status:  "error",
			Message: fmt.Sprintf("invalid pattern: %v", err),
		}
	}

	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	shell, flag := "sh", "-c"
	if runtime.GOOS == "windows" {
		shell, flag = "cmd", "/c"
	}

	cmd := exec.CommandContext(ctx, shell, flag, cfg.Command)
	if opts.WorkspacePath != "" {
		cmd.Dir = opts.WorkspacePath
	}

	output, cmdErr := cmd.CombinedOutput()
	outStr := string(output)

	if ctx.Err() != nil {
		return harness.CheckResult{
			Status:  "error",
			Message: fmt.Sprintf("command timed out after %ds", cfg.TimeoutSec),
			Details: truncate(outStr, 2000),
		}
	}
	if cmdErr != nil {
		return harness.CheckResult{
			Status:  "fail",
			Message: fmt.Sprintf("command failed: %v", cmdErr),
			Details: truncate(outStr, 2000),
		}
	}

	if re.MatchString(outStr) {
		return harness.CheckResult{
			Status:  "pass",
			Message: fmt.Sprintf("output matches pattern %q", cfg.Pattern),
			Details: truncate(outStr, 2000),
		}
	}

	return harness.CheckResult{
		Status:  "fail",
		Message: fmt.Sprintf("output does not match pattern %q", cfg.Pattern),
		Details: truncate(outStr, 2000),
	}
}
