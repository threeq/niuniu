//go:build linux

package agentproxy

import "syscall"

// codexAppServerSysProcAttr asks the kernel to kill the app-server when the
// niuniu server dies (Pdeathsig). Without it, every server restart/update
// orphans every running app-server: the orphans keep their thread-writer
// flocks, and the restarted server's first thread/resume of those sessions
// fails with "already has an active writer" — the team-edition conflict
// cascade. Best-effort: the Go runtime delivers Pdeathsig on parent-THREAD
// death, which is normally the process lifetime.
func codexAppServerSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
