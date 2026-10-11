//go:build linux && !amd64 && !arm64

package sandbox

// An architecture we neither ship nor have a seccomp arch filter for.
// buildAuditArch returns 0 for it, so applySeccomp refuses to install a filter
// and the self-check reports the fence unavailable — fail-closed, not silently
// unfiltered.
func seccompDeniedArchList() []uintptr { return nil }
