//go:build !windows

package service

import (
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// killProcessesInDir finds and terminates all processes whose command line
// contains the given directory path. Even though Unix allows deleting files
// held open by a process, killing workspace processes avoids leaving orphan
// dev servers (e.g. pnpm dev / vite) that continue consuming CPU and memory.
func killProcessesInDir(dir string) {
	if dir == "" {
		return
	}

	// Use pgrep -f with the dir escaped as a literal regex pattern,
	// so metacharacters in the path (e.g. dots, plus signs) don't cause
	// unintended matches against other processes.
	pattern := regexp.QuoteMeta(dir)
	cmd := exec.Command("pgrep", "-f", pattern)
	out, err := cmd.Output()
	if err != nil {
		// pgrep returns exit code 1 when no processes matched — not an error
		return
	}

	pids := parsePIDLines(string(out))
	if len(pids) == 0 {
		return
	}

	selfPID := os.Getpid()

	slog.Info("killProcessesInDir: killing processes in workspace directory",
		"dir", dir, "pids", pids)

	for _, pid := range pids {
		if pid == selfPID {
			continue
		}
		// Send SIGTERM for graceful shutdown
		killCmd := exec.Command("kill", "-TERM", "--", strconv.Itoa(pid))
		if err := killCmd.Run(); err != nil {
			// Try SIGKILL as fallback
			killCmd = exec.Command("kill", "-9", strconv.Itoa(pid))
			if err := killCmd.Run(); err != nil {
				slog.Warn("killProcessesInDir: failed to kill process",
					"pid", pid, "error", err)
			}
		}
	}
}

// parsePIDLines parses newline-separated PID output (one integer per line).
func parsePIDLines(output string) []int {
	var pids []int
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if pid, err := strconv.Atoi(line); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}
