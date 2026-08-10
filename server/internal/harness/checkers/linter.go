package checkers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/harness"
)

const defaultLinterTimeoutSec = 60

type linterConfig struct {
	Command    string `json:"command"`
	TimeoutSec int    `json:"timeout_sec"`
}

type Linter struct{}

func NewLinter() *Linter {
	return &Linter{}
}

func (l *Linter) Name() string { return "linter" }

func (l *Linter) Check(ctx context.Context, opts harness.CheckOpts) harness.CheckResult {
	start := time.Now()

	var cfg linterConfig
	if opts.SpecConfig != "" {
		if err := json.Unmarshal([]byte(opts.SpecConfig), &cfg); err != nil {
			return harness.CheckResult{
				Status:     "error",
				Message:    fmt.Sprintf("invalid config: %v", err),
				DurationMs: time.Since(start).Milliseconds(),
			}
		}
	}

	if cfg.Command == "" {
		return harness.CheckResult{
			Status:     "skip",
			Message:    "no lint command configured",
			DurationMs: time.Since(start).Milliseconds(),
		}
	}

	timeoutSec := cfg.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = defaultLinterTimeoutSec
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	parts := strings.Fields(cfg.Command)
	//nolint:gosec // command comes from trusted spec config
	cmd := exec.CommandContext(timeoutCtx, parts[0], parts[1:]...)
	if opts.WorkspacePath != "" {
		cmd.Dir = opts.WorkspacePath
	}

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	err := cmd.Run()
	elapsed := time.Since(start).Milliseconds()

	if timeoutCtx.Err() == context.DeadlineExceeded {
		return harness.CheckResult{
			Status:     "error",
			Message:    fmt.Sprintf("lint command timed out after %ds", timeoutSec),
			DurationMs: elapsed,
		}
	}

	if err != nil {
		return harness.CheckResult{
			Status:     "fail",
			Message:    "lint command exited with non-zero status",
			Details:    truncate(out.String(), 2000),
			DurationMs: elapsed,
		}
	}

	return harness.CheckResult{
		Status:     "pass",
		Message:    "lint passed",
		DurationMs: elapsed,
	}
}
