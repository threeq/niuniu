//go:build !linux && !darwin

package sandbox

// On Windows the job object reports peak committed memory and the platform
// offers no cheap unprivileged per-process sample, and on any other platform
// there is no fence to sample at all; this therefore returns 0 here ("unknown")
// rather than a misleading number. macOS has its own reader (rss_darwin.go).
func platformRSS(int) int64 { return 0 }
