package checkers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/harness"
)

const (
	defaultCoverageCommand    = "go test -cover ./..."
	defaultCoverageThreshold  = 80.0
	defaultCoverageTimeoutSec = 120
)

var coverageRegex = regexp.MustCompile(`coverage:\s+(\d+\.?\d*)%`)

type testCoverageConfig struct {
	Threshold  float64 `json:"threshold"`
	Command    string  `json:"command"`
	TimeoutSec int     `json:"timeout_sec"`
}

type TestCoverage struct{}

func NewTestCoverage() *TestCoverage {
	return &TestCoverage{}
}

func (t *TestCoverage) Name() string { return "test-coverage" }

func (t *TestCoverage) Check(ctx context.Context, opts harness.CheckOpts) harness.CheckResult {
	start := time.Now()

	cfg := testCoverageConfig{
		Command:    defaultCoverageCommand,
		Threshold:  defaultCoverageThreshold,
		TimeoutSec: defaultCoverageTimeoutSec,
	}

	if opts.SpecConfig != "" {
		var userCfg testCoverageConfig
		if err := json.Unmarshal([]byte(opts.SpecConfig), &userCfg); err != nil {
			return harness.CheckResult{
				Status:     "error",
				Message:    fmt.Sprintf("invalid config: %v", err),
				DurationMs: time.Since(start).Milliseconds(),
			}
		}
		if userCfg.Command != "" {
			cfg.Command = userCfg.Command
		}
		if userCfg.Threshold != 0 {
			cfg.Threshold = userCfg.Threshold
		}
		if userCfg.TimeoutSec > 0 {
			cfg.TimeoutSec = userCfg.TimeoutSec
		}
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSec)*time.Second)
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

	_ = cmd.Run() // coverage may still be printed even on test failure
	elapsed := time.Since(start).Milliseconds()

	if timeoutCtx.Err() == context.DeadlineExceeded {
		return harness.CheckResult{
			Status:     "error",
			Message:    fmt.Sprintf("test command timed out after %ds", cfg.TimeoutSec),
			DurationMs: elapsed,
		}
	}

	output := out.String()
	coverage, ok := ParseCoveragePercent(output)
	if !ok {
		return harness.CheckResult{
			Status:     "error",
			Message:    "could not parse coverage percentage from test output",
			Details:    truncate(output, 2000),
			DurationMs: elapsed,
		}
	}

	if coverage >= cfg.Threshold {
		return harness.CheckResult{
			Status:     "pass",
			Message:    fmt.Sprintf("coverage %.1f%% meets threshold %.1f%%", coverage, cfg.Threshold),
			DurationMs: elapsed,
		}
	}

	return harness.CheckResult{
		Status:     "fail",
		Message:    fmt.Sprintf("coverage %.1f%% is below threshold %.1f%%", coverage, cfg.Threshold),
		Details:    truncate(output, 2000),
		DurationMs: elapsed,
	}
}

// ParseCoveragePercent extracts the coverage percentage from go test -cover output.
// It is exported for testability.
func ParseCoveragePercent(output string) (float64, bool) {
	match := coverageRegex.FindStringSubmatch(output)
	if match == nil {
		return 0, false
	}
	val, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return 0, false
	}
	return val, true
}
