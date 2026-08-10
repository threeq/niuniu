//go:build windows

package service

import (
	"log/slog"
	"os/exec"
	"strconv"
	"time"
)

// signalSubagentPids terminates the given PIDs on Windows using taskkill.
// SIGTERM is not reliably available on Windows, so we use /F (force) with
// a short delay as a courtesy. Best-effort; errors are logged at WARN.
func signalSubagentPids(pids []int) {
	for _, pid := range pids {
		time.AfterFunc(3*time.Second, func() {
			cmd := exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid))
			if err := cmd.Run(); err != nil {
				slog.Warn("cascadeStopSubagents: taskkill failed",
					"pid", pid, "err", err)
			}
		})
	}
}
