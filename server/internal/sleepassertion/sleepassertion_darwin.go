//go:build darwin

package sleepassertion

import "os/exec"

type darwinAssertion struct {
	cmd *exec.Cmd
}

func acquire(reason string) (Assertion, error) {
	// caffeinate -dims: prevent display sleep (-d), idle sleep (-i),
	// disk sleep (-m), and system sleep (-s). Runs until killed.
	cmd := exec.Command("caffeinate", "-dims")
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &darwinAssertion{cmd: cmd}, nil
}

func (a *darwinAssertion) Close() error {
	if a.cmd != nil && a.cmd.Process != nil {
		return a.cmd.Process.Kill()
	}
	return nil
}
