//go:build linux && amd64

package sandbox

import "golang.org/x/sys/unix"

// On x86-64, fork and vfork exist as their own syscalls, so the box may as well
// refuse them outright. (clone stays allowed: the Go runtime creates OS threads
// with it, and the boxed code has no way to call it.)
func seccompDeniedArchList() []uintptr { return []uintptr{unix.SYS_FORK, unix.SYS_VFORK} }
