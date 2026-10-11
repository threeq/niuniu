//go:build !linux

package sandbox

// On Windows the job object reports peak committed memory, and on macOS the
// platform offers no cheap unprivileged per-process sample; both therefore
// return 0 here ("unknown") rather than a misleading number.
func platformRSS(int) int64 { return 0 }
