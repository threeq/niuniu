package niuniuagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// A negative timeout means "no per-request deadline": the caller's ctx is the
// only bound. session/prompt depends on this — it spans a whole agent turn,
// and a fixed cap here re-introduced the hard-ceiling bug that killed
// legitimate long turns with "context deadline exceeded".
func TestRequestNegativeTimeoutWaitsForResponse(t *testing.T) {
	b, _ := newStartedBackend(t, nil)

	type result struct {
		out json.RawMessage
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		out, err := b.request(context.Background(), "session/prompt", map[string]any{}, -1)
		resCh <- result{out, err}
	}()

	// Longer than the positive-timeout path below needs to fire: the pending
	// request must still be alive here, not failed with "timed out".
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case r := <-resCh:
			t.Fatalf("request returned while pending: out=%s err=%v", r.out, r.err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}

	b.mu.Lock()
	if len(b.pending) != 1 {
		b.mu.Unlock()
		t.Fatalf("pending = %d, want 1", len(b.pending))
	}
	var id int64
	for k := range b.pending {
		id = k
	}
	b.mu.Unlock()
	b.handleResponse([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"ok":true}}`, id)), id)

	select {
	case r := <-resCh:
		if r.err != nil {
			t.Fatalf("request err = %v, want nil", r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request never returned after the response arrived")
	}
}

func TestRequestPositiveTimeoutExpires(t *testing.T) {
	b, _ := newStartedBackend(t, nil)

	_, err := b.request(context.Background(), "initialize", map[string]any{}, 30*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want timed out", err)
	}
}
