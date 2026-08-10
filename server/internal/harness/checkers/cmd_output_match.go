package checkers

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/harness"
)

const defaultCmdOutputMatchTimeoutSec = 120

// CmdOutputMatch is a generic typed checker that runs a configured command,
// extracts a numeric value from its combined output via spec.ExtractRegex,
// and compares it against spec.ThresholdValue using spec.ThresholdOp.
type CmdOutputMatch struct{}

// NewCmdOutputMatch returns a new CmdOutputMatch checker.
func NewCmdOutputMatch() *CmdOutputMatch { return &CmdOutputMatch{} }

// Kind reports the registered Kind for this checker.
func (c *CmdOutputMatch) Kind() string { return harness.KindCommandOutputMatch }

// Run executes spec.Command, extracts the first capture group of
// spec.ExtractRegex as float64, and compares it via spec.ThresholdOp.
func (c *CmdOutputMatch) Run(ctx context.Context, spec harness.Spec, env harness.CheckEnv) harness.CheckResult {
	start := time.Now()

	if strings.TrimSpace(spec.Command) == "" {
		return harness.CheckResult{
			Status:     "skip",
			Message:    "no command configured",
			DurationMs: time.Since(start).Milliseconds(),
		}
	}
	if strings.TrimSpace(spec.ExtractRegex) == "" {
		return harness.CheckResult{
			Status:     "skip",
			Message:    "no extract regex configured",
			DurationMs: time.Since(start).Milliseconds(),
		}
	}
	if !harness.IsValidThresholdOp(spec.ThresholdOp) {
		return harness.CheckResult{
			Status:     "error",
			Message:    fmt.Sprintf("invalid threshold op: %q", spec.ThresholdOp),
			DurationMs: time.Since(start).Milliseconds(),
		}
	}

	re, err := regexp.Compile(spec.ExtractRegex)
	if err != nil {
		return harness.CheckResult{
			Status:     "error",
			Message:    fmt.Sprintf("invalid extract regex: %v", err),
			DurationMs: time.Since(start).Milliseconds(),
		}
	}

	timeoutSec := spec.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = defaultCmdOutputMatchTimeoutSec
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	cmd := shellExec(timeoutCtx, spec.Command)
	if env.WorkspacePath != "" {
		cmd.Dir = env.WorkspacePath
	}

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	_ = cmd.Run() // exit code ignored; the value is what matters
	elapsed := time.Since(start).Milliseconds()

	if timeoutCtx.Err() == context.DeadlineExceeded {
		return harness.CheckResult{
			Status:     "error",
			Message:    fmt.Sprintf("command timed out after %ds", timeoutSec),
			Details:    truncate(out.String(), 2000),
			DurationMs: elapsed,
		}
	}

	output := out.String()
	match := re.FindStringSubmatch(output)
	if len(match) < 2 {
		return harness.CheckResult{
			Status:     "error",
			Message:    "extract regex did not match output",
			Details:    truncate(output, 2000),
			DurationMs: elapsed,
		}
	}

	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return harness.CheckResult{
			Status:     "error",
			Message:    fmt.Sprintf("could not parse extracted value %q as float: %v", match[1], err),
			Details:    truncate(output, 2000),
			DurationMs: elapsed,
		}
	}

	if compareThreshold(value, spec.ThresholdValue, spec.ThresholdOp) {
		return harness.CheckResult{
			Status:     "pass",
			Message:    fmt.Sprintf("value %g %s threshold %g", value, spec.ThresholdOp, spec.ThresholdValue),
			DurationMs: elapsed,
		}
	}

	return harness.CheckResult{
		Status:     "fail",
		Message:    fmt.Sprintf("value %g does not satisfy %s %g", value, spec.ThresholdOp, spec.ThresholdValue),
		Details:    truncate(output, 2000),
		DurationMs: elapsed,
	}
}

// compareThreshold applies a comparison operator to two float64 values.
func compareThreshold(value, threshold float64, op string) bool {
	switch op {
	case ">=":
		return value >= threshold
	case "<=":
		return value <= threshold
	case "==":
		return value == threshold
	case "!=":
		return value != threshold
	case ">":
		return value > threshold
	case "<":
		return value < threshold
	default:
		return false
	}
}
