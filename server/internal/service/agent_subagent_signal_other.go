//go:build !windows

package service

import (
	"log/slog"
	"os"
	"syscall"
	"time"
)

// signalSubagentPids sends SIGTERM to each PID, followed by SIGKILL after 3s.
// Best-effort; errors are logged at WARN.
func signalSubagentPids(pids []int) {
	for _, pid := range pids {
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		if err := proc.Signal(syscall.SIGTERM); err != nil {
			slog.Warn("cascadeStopSubagents: SIGTERM failed",
				"pid", pid, "err", err)
		}
		time.AfterFunc(3*time.Second, func() {
			_ = proc.Kill()
		})
	}
}
