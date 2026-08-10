//go:build windows

package agentproxy

import (
	"os/exec"
	"strconv"
	"syscall"
)

// killPIDTree force-kills the process AND all of its descendants. The long-lived
// `claude` process spawns children (Bash tool subprocesses, the niuniu-mcp
// server, any MCP/tool process), and a plain os.Process.Kill (TerminateProcess)
// ends only the single PID — orphaning those children so they keep running and
// holding handles. `taskkill /T` walks the OS parent-child tree and `/F`
// force-terminates the whole subtree, matching the repo convention in
// service/process_cleanup_windows.go.
func killPIDTree(pid int) error {
	cmd := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Run()
}

// newProcessGroupAttr returns the SysProcAttr used when spawning the long-lived
// agent process. On Windows no special attribute is needed: taskkill /T locates
// descendants via the OS parent-child tree, so the parent need not lead a group.
func newProcessGroupAttr() *syscall.SysProcAttr {
	return nil
}
