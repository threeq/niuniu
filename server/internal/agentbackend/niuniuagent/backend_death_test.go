package niuniuagent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/agentbackend"
)

// A backend whose process exited must fail fast with a clear reason instead
// of feeding frames into a dead pipe ("write |1: The pipe is being closed."),
// and Prompt must surface the death as the turn's terminal error.
func TestDeadBackendFailsFast(t *testing.T) {
	b, sb := newStartedBackend(t, nil)
	b.mu.Lock()
	b.exited = true
	b.exitErr = errors.New("exit status 1")
	b.active, b.activeDone = nil, nil // no in-flight turn
	b.mu.Unlock()

	if !b.Dead() {
		t.Fatal("Dead() = false on an exited backend")
	}
	if err := b.write(rpcRequest{JSONRPC: "2.0", Method: "ping"}); err == nil || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("write err = %v, want fast-fail mentioning the exit", err)
	}
	if sb.String() != "" {
		t.Errorf("frames were written to a dead process's stdin: %q", sb.String())
	}

	ch, err := b.Prompt(context.Background(), agentbackend.PromptRequest{Message: "hi"})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	var ev agentbackend.Event
	select {
	case ev = <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("no terminal event from Prompt on a dead backend")
	}
	if ev.Type != agentbackend.EventError || !strings.Contains(ev.Error, "exited") {
		t.Errorf("terminal event = %+v, want an error mentioning the exit", ev)
	}
}

func TestDeadAfterClose(t *testing.T) {
	b, _ := newStartedBackend(t, nil)
	if b.Dead() {
		t.Fatal("Dead() = true on a live backend")
	}
	_ = b.Close(context.Background())
	if !b.Dead() {
		t.Fatal("Dead() = false after Close")
	}
}
