package model

import (
	"fmt"
	"io"
	"os"
	"time"
)

// idleTimeoutReader bounds the IDLE gap of a streaming response: if no byte
// arrives within timeout, Read fails with errStreamIdle and the stream loop
// exits with an error — so a gateway that opens SSE, delivers some deltas,
// then hangs without closing cannot park a turn (and the whole session) in
// "running" forever. A goroutine per Read leaks only until the caller closes
// the response body on the error path, which the stream loops already do.
type idleTimeoutReader struct {
	r       io.Reader
	timeout time.Duration
}

// streamIdleTimeout is the no-data bound for streaming responses.
// NIUNIU_AGENT_STREAM_IDLE_TIMEOUT (Go duration, e.g. 90s) overrides.
func streamIdleTimeout() time.Duration {
	if v := os.Getenv("NIUNIU_AGENT_STREAM_IDLE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 90 * time.Second
}

func newIdleTimeoutReader(r io.Reader) *idleTimeoutReader {
	return &idleTimeoutReader{r: r, timeout: streamIdleTimeout()}
}

func (ir *idleTimeoutReader) Read(p []byte) (int, error) {
	type res struct {
		n   int
		err error
	}
	ch := make(chan res, 1)
	go func() {
		n, err := ir.r.Read(p)
		ch <- res{n, err}
	}()
	t := time.NewTimer(ir.timeout)
	defer t.Stop()
	select {
	case r := <-ch:
		return r.n, r.err
	case <-t.C:
		return 0, errStreamIdle{timeout: ir.timeout}
	}
}

type errStreamIdle struct{ timeout time.Duration }

func (e errStreamIdle) Error() string {
	return fmt.Sprintf("stream idle: no data received within %s (gateway stalled mid-SSE)", e.timeout)
}
