package agentproxy

import (
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// longRunningCmd returns a platform-appropriate command that runs for ~60s, so
// the test can verify killProcess actually terminates a live OS process.
func longRunningCmd() *exec.Cmd {
	if runtime.GOOS == "windows" {
		// ping -n 60 loops ~59s; cmd /c hosts it so there's a parent + child to
		// exercise the taskkill /T tree walk.
		return exec.Command("cmd", "/c", "ping -n 60 127.0.0.1 > NUL")
	}
	return exec.Command("sleep", "60")
}

// TestKillProcess_KillsRealProcess is the end-to-end guard for requirement 1
// ("真实杀死 claude 进程"): killProcess must actually terminate the spawned OS
// process (and, via killPIDTree, its children) — not just flip the alive flag.
func TestKillProcess_KillsRealProcess(t *testing.T) {
	cmd := longRunningCmd()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	cmd.SysProcAttr = newProcessGroupAttr() // same spawn attr ensureProcess uses
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn test process on this host: %v", err)
	}

	s := &WorkspaceSession{
		turnDone: make(chan struct{}, 1),
		cmd:      cmd,
		stdin:    stdin,
		alive:    true,
	}

	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()

	s.killProcess()

	select {
	case <-exited:
		// Process actually terminated — good.
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill() // cleanup so we don't leak the runaway process
		t.Fatal("killProcess did not terminate the real OS process within 5s")
	}

	// killProcess must also unblock a waiter on turnDone (Stop relies on this).
	select {
	case <-s.turnDone:
	default:
		t.Error("killProcess did not signal turnDone")
	}
}
