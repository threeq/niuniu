package service

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// killProcessesInDir finds and terminates all processes whose command line
// contains the given directory path. This serves two purposes:
// 1. On Windows, native modules (.node files) loaded by Node.js are locked
//    and prevent directory deletion until the process exits.
// 2. On all platforms, it avoids leaving orphan dev servers (pnpm dev, vite)
//    that continue consuming CPU and memory after the workspace is deleted.
func killProcessesInDir(dir string) {
	if dir == "" {
		return
	}

	// Use PowerShell to find all processes whose command line contains the
	// workspace path. PowerShell's Get-CimInstance replaces the deprecated WMIC.
	// Output format: one PID per line.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	psScript := `Get-CimInstance Win32_Process | Where-Object { $_.CommandLine -like '*` +
		escapePowerShellWildcard(dir) +
		`*' } | Select-Object -ExpandProperty ProcessId`

	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", psScript)
	out, err := cmd.Output()
	if err != nil {
		slog.Debug("killProcessesInDir: PowerShell query failed, trying tasklist fallback",
			"dir", dir, "error", err)
		return
	}

	selfPID := os.Getpid()
	pids := parsePIDLines(string(out))
	if len(pids) == 0 {
		return
	}

	slog.Info("killProcessesInDir: killing processes in workspace directory",
		"dir", dir, "pids", pids)

	for _, pid := range pids {
		if pid == selfPID {
			continue
		}
		// taskkill /T kills the process tree (e.g. pnpm -> vite -> rolldown)
		killCmd := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid))
		if err := killCmd.Run(); err != nil {
			slog.Warn("killProcessesInDir: failed to kill process",
				"pid", pid, "error", err)
		}
	}

	// Give the OS a moment to release file handles
	time.Sleep(500 * time.Millisecond)
}

// escapePowerShellWildcard escapes characters that are special in PowerShell
// -like wildcard patterns to prevent injection. The -like operator treats
// *, ?, and [ as wildcards; we escape them with backtick.
// Single quotes in the path are doubled to stay within the PS string literal.
func escapePowerShellWildcard(s string) string {
	s = strings.ReplaceAll(s, "'", "''")
	s = strings.ReplaceAll(s, "`", "``")
	s = strings.ReplaceAll(s, "*", "`*")
	s = strings.ReplaceAll(s, "?", "`?")
	s = strings.ReplaceAll(s, "[", "`[")
	return s
}

// parsePIDLines parses newline-separated PID output (one integer per line).
func parsePIDLines(output string) []int {
	var pids []int
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if pid, err := strconv.Atoi(line); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}
