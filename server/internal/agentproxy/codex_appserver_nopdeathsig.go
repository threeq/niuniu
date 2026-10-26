//go:build !linux

package agentproxy

import "syscall"

// Non-Linux platforms either lack Pdeathsig (darwin, windows) or are not
// deployment targets; Close()'s explicit teardown remains the lifecycle path.
func codexAppServerSysProcAttr() *syscall.SysProcAttr { return nil }
