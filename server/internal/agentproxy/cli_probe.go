package agentproxy

import (
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"
)

// cliAvailable is a process-wide flag set by ProbeClaudeCLI at server start
// and refreshed on demand. Atomic for lock-free reads. Consumed by the
// claude-backed suggestion helpers (goal_condition_suggest, column_op_suggest,
// kanban/prompt-gen handlers) and the /health probe.
var cliAvailable atomic.Bool

// ClaudeCLIAvailable returns the cached probe result.
func ClaudeCLIAvailable() bool { return cliAvailable.Load() }

// SetClaudeCLIAvailableForTest forces the cached probe value, returning the
// previous value so callers can defer-restore. Intended only for tests that
// need to drive the 503 / CLI_UNAVAILABLE branch without invoking the real
// claude binary.
func SetClaudeCLIAvailableForTest(v bool) bool {
	return cliAvailable.Swap(v)
}

// claudeFlagsRequired lists the flags the claude-backed helpers (one-shot
// suggestion / generation calls) rely on. Probing for their presence in
// `claude --help` catches CLI-upgrade regressions early — a missing flag would
// otherwise silently break those calls at runtime.
var claudeFlagsRequired = []string{
	"--model",
	"--output-format",
	"--no-session-persistence",
	"--disable-slash-commands",
	"--max-budget-usd",
	"-p",
}

// missingFlags returns the subset of required flags not present as substrings
// of help. An empty result means every required flag is present.
func missingFlags(help string, required []string) []string {
	var missing []string
	for _, f := range required {
		if !strings.Contains(help, f) {
			missing = append(missing, f)
		}
	}
	return missing
}

// ProbeClaudeCLI runs `claude --version` AND `claude --help` with a 5s
// timeout each, sets the availability flag only when both succeed and every
// flag in claudeFlagsRequired appears in the help text. The flag is consumed by
// the one-shot suggestion/generation helpers and the /health probe. Safe to
// call repeatedly; only logs on state changes.
func ProbeClaudeCLI(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := exec.CommandContext(ctx, "claude", "--version").Run(); err != nil {
		flipCLIAvailable(false, "version probe failed: "+err.Error())
		return
	}

	helpOut, err := exec.CommandContext(ctx, "claude", "--help").Output()
	if err != nil {
		flipCLIAvailable(false, "help probe failed: "+err.Error())
		return
	}
	if missing := missingFlags(string(helpOut), claudeFlagsRequired); len(missing) > 0 {
		flipCLIAvailable(false, "claude CLI missing required flags: "+strings.Join(missing, ","))
		return
	}
	flipCLIAvailable(true, "")
}

func flipCLIAvailable(ok bool, reason string) {
	prev := cliAvailable.Load()
	cliAvailable.Store(ok)
	if prev != ok {
		if ok {
			slog.Info("claude CLI probe passed (all required flags present)")
		} else {
			slog.Warn("claude CLI probe failed; one-shot suggestion helpers disabled until next probe", "reason", reason)
		}
	}
}
