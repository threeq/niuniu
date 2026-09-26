package model

import (
	"fmt"
	"os"
	"time"
)

// streamIdleTimeout is the no-DATA-frame bound for streaming responses.
// Gateways keep SSE alive with heartbeat bytes (pings, blank lines) even
// when the model stopped producing, so the deadline resets only on real
// data frames (see scanSSE). NIUNIU_AGENT_STREAM_IDLE_TIMEOUT (Go duration,
// e.g. 90s) overrides; default 90s.
func streamIdleTimeout() time.Duration {
	if v := os.Getenv("NIUNIU_AGENT_STREAM_IDLE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 90 * time.Second
}

// errStreamIdle is returned when the data-frame idle deadline fires.
type errStreamIdle struct{ timeout time.Duration }

func (e errStreamIdle) Error() string {
	return fmt.Sprintf("stream idle: no data frame within %s (gateway stalled mid-SSE)", e.timeout)
}
