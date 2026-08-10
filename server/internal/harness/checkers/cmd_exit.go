package checkers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/harness"
)

// shellExec returns a CommandContext that runs `command` through the platform's
// default shell so quoted args (`sh -c "exit 2"`, `pnpm test --reporter=verbose`)
// behave as users expect. On Windows uses cmd.exe /c, elsewhere /bin/sh -c.
// command is treated as trusted spec config (operator-set).
func shellExec(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		//nolint:gosec // command is trusted spec config
		return exec.CommandContext(ctx, "cmd.exe", "/c", command)
	}
	//nolint:gosec // command is trusted spec config
	return exec.CommandContext(ctx, "/bin/sh", "-c", command)
}

const defaultCmdExitTimeoutSec = 60

// CmdExit is a generic typed checker that runs a configured command and
// compares its exit code to spec.ExpectedExitCode.
type CmdExit struct{}

// NewCmdExit returns a new CmdExit checker.
func NewCmdExit() *CmdExit { return &CmdExit{} }

// Kind reports the registered Kind for this checker.
func (c *CmdExit) Kind() string { return harness.KindCommandExitCode }

// Run executes spec.Command and compares the actual exit code against
// spec.ExpectedExitCode.
func (c *CmdExit) Run(ctx context.Context, spec harness.Spec, env harness.CheckEnv) harness.CheckResult {
	start := time.Now()

	if strings.TrimSpace(spec.Command) == "" {
		return harness.CheckResult{
			Status:     "skip",
			Message:    "no command configured",
			DurationMs: time.Since(start).Milliseconds(),
		}
	}

	timeoutSec := spec.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = defaultCmdExitTimeoutSec
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

	runErr := cmd.Run()
	elapsed := time.Since(start).Milliseconds()

	if timeoutCtx.Err() == context.DeadlineExceeded {
		return harness.CheckResult{
			Status:     "error",
			Message:    fmt.Sprintf("command timed out after %ds", timeoutSec),
			Details:    truncate(out.String(), 2000),
			DurationMs: elapsed,
		}
	}

	// Distinguish "couldn't start" (no ProcessState) from "ran but non-zero".
	var exitErr *exec.ExitError
	if runErr != nil && !errors.As(runErr, &exitErr) {
		return harness.CheckResult{
			Status:     "error",
			Message:    fmt.Sprintf("failed to run command: %v", runErr),
			Details:    truncate(out.String(), 2000),
			DurationMs: elapsed,
		}
	}

	actualExit := 0
	if cmd.ProcessState != nil {
		actualExit = cmd.ProcessState.ExitCode()
	}

	if actualExit == spec.ExpectedExitCode {
		return harness.CheckResult{
			Status:     "pass",
			Message:    fmt.Sprintf("command exited with code %d as expected", actualExit),
			DurationMs: elapsed,
		}
	}

	return harness.CheckResult{
		Status:     "fail",
		Message:    fmt.Sprintf("command exited with code %d, expected %d", actualExit, spec.ExpectedExitCode),
		Details:    truncate(out.String(), 2000),
		DurationMs: elapsed,
	}
}
