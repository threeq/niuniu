//go:build linux

package sleepassertion

import "os/exec"

type linuxAssertion struct {
	cmd *exec.Cmd
}

// acquire uses systemd-inhibit to hold an idle-sleep inhibitor lock.
// The child process keeps the lock open; killing it releases the lock.
// If systemd-inhibit is not available the command fails at Start() and the
// caller receives the error, which is tolerated (session proceeds without guard).
func acquire(reason string) (Assertion, error) {
	cmd := exec.Command(
		"systemd-inhibit",
		"--what=idle",
		"--who=niuniu-desktop",
		"--why="+reason,
		"--mode=block",
		"sleep", "infinity",
	)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &linuxAssertion{cmd: cmd}, nil
}

func (a *linuxAssertion) Close() error {
	if a.cmd != nil && a.cmd.Process != nil {
		return a.cmd.Process.Kill()
	}
	return nil
}
