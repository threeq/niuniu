package checkers

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"github.com/niuniu-dev/niuniu/internal/harness"
)

type commandExitCodeConfig struct {
	Command    string `json:"command"`
	TimeoutSec int    `json:"timeout_sec"`
}

// CommandExitCode runs a shell command and checks exit code = 0.
// Config: {"command": "pnpm test", "timeout_sec": 120}
type CommandExitCode struct{}

func (c *CommandExitCode) Name() string { return "command-exit-code" }

func (c *CommandExitCode) Check(ctx context.Context, opts harness.CheckOpts) harness.CheckResult {
	var cfg commandExitCodeConfig
	if opts.SpecConfig != "" {
		json.Unmarshal([]byte(opts.SpecConfig), &cfg)
	}
	if cfg.Command == "" {
		return harness.CheckResult{Status: "skip", Message: "no command configured"}
	}
	if cfg.TimeoutSec <= 0 {
		cfg.TimeoutSec = 120
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

	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return harness.CheckResult{
			Status:  "error",
			Message: fmt.Sprintf("command timed out after %ds", cfg.TimeoutSec),
			Details: truncate(string(output), 2000),
		}
	}
	if err != nil {
		return harness.CheckResult{
			Status:  "fail",
			Message: fmt.Sprintf("command exited with error: %v", err),
			Details: truncate(string(output), 2000),
		}
	}

	return harness.CheckResult{
		Status:  "pass",
		Message: "command exited successfully",
		Details: truncate(string(output), 2000),
	}
}
