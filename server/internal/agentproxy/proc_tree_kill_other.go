//go:build !windows

package agentproxy

import "syscall"

// killPIDTree force-kills the process AND all of its descendants. The long-lived
// agent process is spawned as its own process-group leader (see
// newProcessGroupAttr), so signalling the negative PID delivers SIGKILL to the
// entire group — `claude` plus every child it forked (Bash tool subprocesses,
// the niuniu-mcp server, any MCP/tool process). A plain os.Process.Kill would
// end only the leader and orphan the rest. Falls back to the single PID if the
// group signal fails (e.g. the leader already reaped the group).
func killPIDTree(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		return syscall.Kill(pid, syscall.SIGKILL)
	}
	return nil
}

// newProcessGroupAttr makes the spawned process a new process-group leader so
// killPIDTree can signal the whole group (parent + all children) at once.
func newProcessGroupAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
