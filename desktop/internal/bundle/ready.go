package bundle

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// readReady scans r line-by-line looking for a single JSON envelope
// with {"event":"ready","addr":"..."}. It gives up after timeout and
// returns ErrReadyTimeout (or a parse/read error).
//
// The caller is responsible for closing r (typically a stdout pipe).
func readReady(r io.Reader, timeout time.Duration) (string, error) {
	type result struct {
		addr string
		err  error
	}
	done := make(chan result, 1)

	go func() {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		if !sc.Scan() {
			if err := sc.Err(); err != nil {
				done <- result{err: fmt.Errorf("read ready: %w", err)}
				return
			}
			done <- result{err: ErrServerExitEarly}
			return
		}
		line := sc.Bytes()
		var payload struct {
			Event string `json:"event"`
			Addr  string `json:"addr"`
		}
		if err := json.Unmarshal(line, &payload); err != nil {
			done <- result{err: fmt.Errorf("parse ready JSON: %w", err)}
			return
		}
		if payload.Event != "ready" {
			done <- result{err: fmt.Errorf("unexpected event %q", payload.Event)}
			return
		}
		done <- result{addr: payload.Addr}
	}()

	select {
	case r := <-done:
		return r.addr, r.err
	case <-time.After(timeout):
		return "", ErrReadyTimeout
	}
}
