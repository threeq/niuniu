package sandbox

import "time"

// Hard ceilings. A draft may ask for less than these but never more: the
// numbers are part of the security posture, not preferences.
const (
	MaxCodeBytes = 256 << 10 // 256 KiB of source

	maxTimeoutMS = 120_000 // 2 minutes of wall clock
	maxMemoryMB  = 1024
	maxOutputB   = 4 << 20 // a single result frame
	maxLogBytes  = 64 << 10
	maxEgress    = 200 // outbound calls per single invocation
)

// Limits are the per-invocation resource ceilings. Zero values mean "use the
// default", so a draft can specify only what it cares about.
type Limits struct {
	// TimeoutMS bounds total wall clock, measured by the host supervisor. It is
	// the backstop for a runaway loop; the box interrupts the interpreter at the
	// same deadline so a busy loop dies instead of merely being killed later.
	TimeoutMS int `json:"timeout_ms"`
	// MemoryMB bounds the box's committed memory. Enforced by the OS fence
	// (job object on Windows, rlimit/linear memory elsewhere) — not by the
	// interpreter, so a runtime that ignores it still cannot exceed it.
	MemoryMB int `json:"memory_mb"`
	// MaxOutputBytes bounds the JSON result the box may return.
	MaxOutputBytes int `json:"max_output_bytes"`
	// MaxLogBytes bounds captured console output.
	MaxLogBytes int `json:"max_log_bytes"`
	// MaxEgress bounds outbound calls from ONE invocation.
	MaxEgress int `json:"max_egress"`
	// MaxRequestBytes bounds one outbound request body.
	MaxRequestBytes int `json:"max_request_bytes"`
	// MaxResponseBytes bounds one inbound response body handed to the box.
	MaxResponseBytes int `json:"max_response_bytes"`
}

// Defaults returns the personal-edition defaults: a function that talks to one
// API twice does not need more than this.
func DefaultLimits() Limits {
	return Limits{
		TimeoutMS:        10_000,
		MemoryMB:         128,
		MaxOutputBytes:   256 << 10,
		MaxLogBytes:      maxLogBytes,
		MaxEgress:        20,
		MaxRequestBytes:  256 << 10,
		MaxResponseBytes: 2 << 20,
	}
}

// normalized clamps every field into (default, ceiling].
func (l Limits) normalized() Limits {
	d := DefaultLimits()
	if l.TimeoutMS <= 0 {
		l.TimeoutMS = d.TimeoutMS
	}
	if l.TimeoutMS > maxTimeoutMS {
		l.TimeoutMS = maxTimeoutMS
	}
	if l.MemoryMB <= 0 {
		l.MemoryMB = d.MemoryMB
	}
	if l.MemoryMB > maxMemoryMB {
		l.MemoryMB = maxMemoryMB
	}
	if l.MaxOutputBytes <= 0 {
		l.MaxOutputBytes = d.MaxOutputBytes
	}
	if l.MaxOutputBytes > maxOutputB {
		l.MaxOutputBytes = maxOutputB
	}
	if l.MaxLogBytes <= 0 {
		l.MaxLogBytes = d.MaxLogBytes
	}
	if l.MaxLogBytes > maxLogBytes {
		l.MaxLogBytes = maxLogBytes
	}
	if l.MaxEgress <= 0 {
		l.MaxEgress = d.MaxEgress
	}
	if l.MaxEgress > maxEgress {
		l.MaxEgress = maxEgress
	}
	if l.MaxRequestBytes <= 0 {
		l.MaxRequestBytes = d.MaxRequestBytes
	}
	if l.MaxRequestBytes > maxOutputB {
		l.MaxRequestBytes = maxOutputB
	}
	if l.MaxResponseBytes <= 0 {
		l.MaxResponseBytes = d.MaxResponseBytes
	}
	if l.MaxResponseBytes > maxFrameBytes {
		l.MaxResponseBytes = maxFrameBytes
	}
	return l
}

// Timeout is the wall-clock budget as a duration.
func (l Limits) Timeout() time.Duration {
	return time.Duration(l.normalized().TimeoutMS) * time.Millisecond
}

// MemoryBytes is the memory ceiling in bytes.
func (l Limits) MemoryBytes() int64 { return int64(l.normalized().MemoryMB) << 20 }
