package agentproxy

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
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

// niuniuAgentAvailable backs the one-shot suggestion helpers (AI suggest /
// classify / column-op), which run on niuniu-agent - niuniu's own binary, so
// hosts without a claude login (or without claude at all) keep working.
var niuniuAgentAvailable atomic.Bool

// niuniuAgentConfigured is agent.niuniu_agent_cli.command from config.yaml,
// set once at server wiring. Empty keeps resolveNiuniuAgentCommand's
// auto-discovery (sidecar dir next to the server, desktop unpack dir, manual
// install location, then PATH).
var niuniuAgentConfigured string

// SetNiuniuAgentConfiguredCommand records the configured niuniu-agent command
// so probes and one-shot spawns resolve the SAME binary the interactive agent
// paths use. Call before ProbeNiuniuAgentCLI.
func SetNiuniuAgentConfiguredCommand(cmd string) { niuniuAgentConfigured = cmd }

// NiuniuAgentBinary returns the resolved niuniu-agent executable, or "" when
// nothing was found (callers then fall back to the bare PATH name).
func NiuniuAgentBinary() string { return resolveNiuniuAgentCommand(niuniuAgentConfigured) }

// NiuniuAgentAvailable returns the cached probe result.
func NiuniuAgentAvailable() bool { return niuniuAgentAvailable.Load() }

// SetNiuniuAgentAvailableForTest forces the cached probe value, returning the
// previous value so callers can defer-restore.
func SetNiuniuAgentAvailableForTest(v bool) bool { return niuniuAgentAvailable.Swap(v) }

// ProbeNiuniuAgentCLI resolves the niuniu-agent binary the SAME way the
// interactive agent paths do (configured command -> sidecar dir next to the
// server -> desktop unpack dir -> manual install dir -> PATH) and records
// availability. An earlier version only did exec.LookPath("niuniu-agent"),
// which failed on every desktop install: the binary is unpacked to
// ~/.niuniu/desktop-v2/sidecars/, a directory that is NOT on PATH - so AI
// suggest reported "CLI unavailable" even though the binary was right there.
// No flag matrix here (unlike claude): the binary ships with niuniu itself, so
// presence is the whole contract.
func ProbeNiuniuAgentCLI() {
	resolved := NiuniuAgentBinary()
	var err error
	switch {
	case resolved == "":
		// Auto-discovery found nothing; let the OS try PATH as a last resort.
		_, err = exec.LookPath("niuniu-agent")
		if err != nil {
			err = errors.New("niuniu-agent not found (config command empty, no sidecar/install copy, not on PATH)")
		}
	case filepath.IsAbs(resolved):
		if _, statErr := os.Stat(resolved); statErr != nil {
			err = statErr
		}
	default:
		_, err = exec.LookPath(resolved)
	}
	ok := err == nil
	prev := niuniuAgentAvailable.Load()
	niuniuAgentAvailable.Store(ok)
	if prev != ok {
		if ok {
			slog.Info("niuniu-agent CLI probe passed", "binary", resolved)
		} else {
			slog.Warn("niuniu-agent CLI probe failed; one-shot suggestion helpers disabled until next probe",
				"binary", resolved, "reason", err.Error())
		}
	}
}

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
