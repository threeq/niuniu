package niuniuagent

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/agentbackend"
)

// newStartedBackend builds a Backend with a fake stdin and an active turn, so
// dispatch-level handlers can be driven directly.
func newStartedBackend(t *testing.T, resolve func(context.Context, agentbackend.PermissionRequest) (agentbackend.PermissionDecision, error)) (*Backend, *syncBuffer) {
	t.Helper()
	b := New(Options{ResolvePermission: resolve})
	sb := &syncBuffer{}
	b.stdin = sb
	b.started = true
	b.session = "s-1"
	b.active = make(chan agentbackend.Event, 64)
	done := make(chan struct{})
	b.activeDone = done
	t.Cleanup(func() { b.finishTurn(agentbackend.Event{Type: agentbackend.EventError, Error: "test end"}) })
	return b, sb
}

// syncBuffer is a mutex-guarded buffer (dispatch writes from a goroutine).
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// Close satisfies io.WriteCloser (Backend.stdin's type).
func (s *syncBuffer) Close() error { return nil }

func TestHandleSessionUpdateMapping(t *testing.T) {
	b, _ := newStartedBackend(t, nil)

	frames := []string{
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s-1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"reasoning..."}}}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hello"}}}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s-1","update":{"sessionUpdate":"tool_call","toolCallId":"t1","title":"Read","kind":"read","status":"in_progress"}}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s-1","update":{"sessionUpdate":"tool_call_update","toolCallId":"t1","status":"completed","output":{"type":"text","text":"file body"}}}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s-1","update":{"sessionUpdate":"tool_call_update","toolCallId":"t2","status":"failed","output":{"type":"text","text":"boom"}}}}`,
	}
	for _, f := range frames {
		b.dispatch([]byte(f))
	}

	expect := []agentbackend.Event{
		{Type: agentbackend.EventThinking, Thinking: "reasoning..."},
		{Type: agentbackend.EventText, Text: "hello"},
		{Type: agentbackend.EventToolUse, ToolName: "Read", ToolUseID: "t1"},
		{Type: agentbackend.EventToolResult, ToolUseID: "t1", Text: "file body"},
		{Type: agentbackend.EventToolResult, ToolUseID: "t2", Text: "boom", IsError: true},
	}
	for i, want := range expect {
		select {
		case got := <-b.active:
			if got.Type != want.Type || got.Text != want.Text || got.ToolName != want.ToolName ||
				got.ToolUseID != want.ToolUseID || got.IsError != want.IsError {
				t.Errorf("event %d = %+v, want %+v", i, got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("event %d never arrived", i)
		}
	}
}

func TestRequestPermissionAllow(t *testing.T) {
	var got agentbackend.PermissionRequest
	b, sb := newStartedBackend(t, func(_ context.Context, req agentbackend.PermissionRequest) (agentbackend.PermissionDecision, error) {
		got = req
		return agentbackend.PermissionDecision{Confirmed: true}, nil
	})

	b.dispatch([]byte(`{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{"sessionId":"s-1","toolCallId":"t1","title":"Write","kind":"edit","options":[]}}`))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && got.Title == "" {
		time.Sleep(10 * time.Millisecond)
	}
	if got.Title != "Write" || got.ID != "t1" {
		t.Errorf("host bridge got %+v", got)
	}
	out := sb.String()
	if !strings.Contains(out, `"optionId":"allow_once"`) || !strings.Contains(out, `"id":7`) {
		t.Errorf("reply frame = %s", out)
	}
}

func TestRequestPermissionFailsClosed(t *testing.T) {
	b, sb := newStartedBackend(t, nil) // nil bridge → deny

	b.dispatch([]byte(`{"jsonrpc":"2.0","id":9,"method":"session/request_permission","params":{"sessionId":"s-1","toolCallId":"t1","title":"Bash","kind":"execute","options":[]}}`))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(sb.String(), "reject_once") {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(sb.String(), `"optionId":"reject_once"`) {
		t.Errorf("nil bridge must deny, frame = %s", sb.String())
	}
}

func TestPromptRequestShape(t *testing.T) {
	b, sb := newStartedBackend(t, nil)

	// Drive Prompt with an already-cancelled ctx so the request aborts
	// immediately after being written — we only want to inspect the frame.
	// Prompt requires no in-flight turn; the helper pre-arms one, so clear it.
	b.mu.Lock()
	b.active = nil
	b.activeDone = nil
	b.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ch, err := b.Prompt(ctx, agentbackend.PromptRequest{Message: "hi"})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	<-ch // terminal event

	out := sb.String()
	if !strings.Contains(out, `"method":"session/prompt"`) ||
		!strings.Contains(out, `"sessionId":"s-1"`) ||
		!strings.Contains(out, `{"type":"text","text":"hi"}`) {
		t.Errorf("prompt frame = %s", out)
	}
}

func TestPromptUsageFillsDoneEvent(t *testing.T) {
	b, sb := newStartedBackend(t, nil)
	b.mu.Lock()
	b.active = nil
	b.activeDone = nil
	b.mu.Unlock()

	// Prompt blocks until the response arrives, so drive it off the test
	// goroutine and answer it from here.
	chCh := make(chan (<-chan agentbackend.Event), 1)
	go func() {
		ch, err := b.Prompt(context.Background(), agentbackend.PromptRequest{Message: "hi"})
		if err != nil {
			t.Errorf("Prompt: %v", err)
			chCh <- nil
			return
		}
		chCh <- ch
	}()

	// Wait for the session/prompt request frame, then answer it with a
	// usage-carrying result (request ids start at 1; Start() is not called).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(sb.String(), `"method":"session/prompt"`) {
		time.Sleep(5 * time.Millisecond)
	}
	b.handleResponse([]byte(`{"jsonrpc":"2.0","id":1,"result":{"stopReason":"end_turn",`+
		`"usage":{"inputTokens":120,"outputTokens":30,"cacheReadTokens":77}}}`), 1)

	ch := <-chCh
	if ch == nil {
		t.Fatal("Prompt failed")
	}
	var done *agentbackend.Event
	for ev := range ch {
		if ev.Type == agentbackend.EventDone {
			e := ev
			done = &e
		}
	}
	if done == nil {
		t.Fatal("no EventDone event")
	}
	if done.InputTokens != 120 || done.OutputTokens != 30 || done.CacheReadTokens != 77 {
		t.Errorf("done event tokens = in:%d out:%d cacheRead:%d, want 120/30/77",
			done.InputTokens, done.OutputTokens, done.CacheReadTokens)
	}
	if done.DurationMs <= 0 {
		t.Errorf("done event DurationMs = %d, want > 0", done.DurationMs)
	}
}
