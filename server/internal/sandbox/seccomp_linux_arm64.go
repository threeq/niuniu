//go:build linux && arm64

package sandbox

// arm64 has no fork or vfork syscall — only clone, which is deliberately
// allowed because the Go runtime uses it for OS threads. There is nothing to
// add here; the common denylist carries the rest.
func seccompDeniedArchList() []uintptr { return nil }
