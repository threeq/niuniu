package agentproxy

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestCodexAppServerClient_ReadLoopSeparatesServerRequestsFromResponses(t *testing.T) {
	respCh := make(chan codexAppServerResponse, 1)
	client := &codexAppServerClient{
		pending: map[int64]chan codexAppServerResponse{1: respCh},
		events:  make(chan codexAppServerNotification, 2),
		done:    make(chan struct{}),
	}
	input := strings.Join([]string{
		`{"id":1,"result":{"ok":true}}`,
		`{"id":99,"method":"item/commandExecution/requestApproval","params":{"command":"echo hi"}}`,
		`{"method":"thread/started","params":{"thread":{"id":"thread-1"}}}`,
		``,
	}, "\n")
	client.readLoop(strings.NewReader(input))

	select {
	case resp := <-respCh:
		if resp.ID != 1 || string(resp.Result) != `{"ok":true}` {
			t.Fatalf("unexpected response: %+v", resp)
		}
	default:
		t.Fatalf("missing response")
	}

	req := <-client.events
	if string(req.ID) != "99" || req.Method != "item/commandExecution/requestApproval" {
		t.Fatalf("server request misrouted: %+v", req)
	}
	notif := <-client.events
	if len(notif.ID) != 0 || notif.Method != "thread/started" {
		t.Fatalf("notification misrouted: %+v", notif)
	}
}

func TestCodexAppServerApprovalResponseShapes(t *testing.T) {
	if got := codexAppServerApprovalResponse("item/commandExecution/requestApproval", true)["decision"]; got != "accept" {
		t.Fatalf("command approval decision=%v want accept", got)
	}
	if got := codexAppServerApprovalResponse("item/fileChange/requestApproval", false)["decision"]; got != "decline" {
		t.Fatalf("file denial decision=%v want decline", got)
	}
	if got := codexAppServerApprovalResponse("execCommandApproval", true)["decision"]; got != "approved" {
		t.Fatalf("legacy exec approval decision=%v want approved", got)
	}
	if got := codexAppServerApprovalResponse("applyPatchApproval", false)["decision"]; got != "denied" {
		t.Fatalf("legacy patch denial decision=%v want denied", got)
	}
}

func TestCodexAppServerSandboxPolicy_EmptyWritableRootsIsSequence(t *testing.T) {
	policy := codexAppServerSandboxPolicy("workspace-write", nil)
	b, err := json.Marshal(policy)
	if err != nil {
		t.Fatalf("marshal policy: %v", err)
	}
	if strings.Contains(string(b), `"writableRoots":null`) {
		t.Fatalf("writableRoots encoded as null: %s", b)
	}
	if !strings.Contains(string(b), `"writableRoots":[]`) {
		t.Fatalf("writableRoots should encode as empty sequence: %s", b)
	}
}

func TestCodexAppendRuntimeRoot_DedupesAndSkipsBlank(t *testing.T) {
	roots := codexAppendRuntimeRoot(nil, " C:/workspace ")
	roots = codexAppendRuntimeRoot(roots, "")
	roots = codexAppendRuntimeRoot(roots, "C:/workspace")
	roots = codexAppendRuntimeRoot(roots, "C:/repos/main")
	if len(roots) != 2 {
		t.Fatalf("roots=%v want 2 entries", roots)
	}
	if roots[0] != "C:/workspace" || roots[1] != "C:/repos/main" {
		t.Fatalf("unexpected roots order/content: %v", roots)
	}
}

func TestCodexAppServerClient_StartThreadSmoke(t *testing.T) {
	codexPath, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex binary not on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client, err := startCodexAppServerClient(ctx, codexPath, os.Environ())
	if err != nil {
		t.Fatalf("startCodexAppServerClient: %v", err)
	}
	defer client.Close()

	cwd := t.TempDir()
	resp, err := client.StartThread(ctx, codexAppServerThreadStartParams{
		Cwd:                cwd,
		Ephemeral:          true,
		SessionStartSource: "startup",
		Sandbox:            "danger-full-access",
		RuntimeRoots:       []string{cwd},
	})
	if err != nil {
		t.Fatalf("StartThread: %v", err)
	}
	if resp.Thread.ID == "" || resp.Thread.SessionID == "" {
		t.Fatalf("thread response missing ids: %+v", resp.Thread)
	}
	if resp.Thread.Cwd != cwd {
		t.Fatalf("thread cwd=%q want %q", resp.Thread.Cwd, cwd)
	}

	sawThreadStarted := false
	for !sawThreadStarted {
		select {
		case ev, ok := <-client.Events():
			if !ok {
				t.Fatalf("app-server events closed before thread/started")
			}
			if ev.Method == "thread/started" {
				var p struct {
					Thread struct {
						ID string `json:"id"`
					} `json:"thread"`
				}
				if err := json.Unmarshal(ev.Params, &p); err != nil {
					t.Fatalf("decode thread/started params: %v", err)
				}
				if p.Thread.ID == resp.Thread.ID {
					sawThreadStarted = true
				}
			}
		case <-ctx.Done():
			t.Fatalf("timeout waiting for thread/started")
		}
	}
}

// TestReadLoopNeverDropsTerminalNotifications is the regression for the
// team-edition "turn Done never fires, every message queues forever" bug:
// readLoop delivered notifications with a NON-BLOCKING send + default-discard.
// During a long turn the events channel (cap 128) filled up with delta/item
// traffic, and the turn's FINAL `turn/completed` — the one notification that
// unblocks waitForTurnComplete — was silently thrown away. The SendLoop then
// stayed "running" forever and every later message queued behind it.
//
// The contract now: terminal notifications (turn/completed, turn/failed,
// process/exited, error) are delivered with a blocking send — they must never
// be dropped; only lossy stream traffic (deltas) may be shed under load.
func TestReadLoopNeverDropsTerminalNotifications(t *testing.T) {
	c := &codexAppServerClient{
		pending: map[int64]chan codexAppServerResponse{},
		events:  make(chan codexAppServerNotification, 8),
		done:    make(chan struct{}),
	}

	// Saturate the channel with lossy stream traffic.
	for i := 0; i < cap(c.events); i++ {
		c.events <- codexAppServerNotification{Method: "item/agentMessage/delta", Params: json.RawMessage(`{"delta":"x"}`)}
	}

	// Feed a terminal notification through readLoop. With the old
	// non-blocking delivery this line was a silent no-op.
	r, w := io.Pipe()
	go func() {
		_, _ = w.Write([]byte(`{"method":"turn/completed","params":{"turn":{"status":"completed","durationMs":42}}}` + "\n"))
		w.Close()
	}()
	go c.readLoop(r)

	// Give readLoop time to process the input line BEFORE draining: under the
	// old non-blocking delivery the saturated channel makes it drop the
	// terminal notification right here (and exit at EOF). Delaying the drain
	// removes the race where an early consumer would accidentally rescue the
	// send.
	time.Sleep(200 * time.Millisecond)

	// Drain: the filler deltas may be shed (allowed), but the terminal
	// notification MUST arrive.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-c.events:
			if !ok {
				t.Fatal("events closed before turn/completed was delivered")
			}
			if ev.Method == "turn/completed" {
				return // delivered — the contract holds
			}
		case <-deadline:
			t.Fatal("turn/completed was dropped under channel saturation — terminal notifications must be delivered with a blocking send")
		}
	}
}

// TestCodexEventLoop_SurvivesIdleGap is the regression for the deployed
// "发消息直接错误: Codex app-server exited unexpectedly" breakage: the batch
// drain treated a momentarily-EMPTY events channel (select default) the same
// as a CLOSED one, so the whole event loop exited at the first gap between
// notifications — which always exists right after thread/started — and the
// exit path reported the still-alive app-server as crashed. The loop must
// only end when the channel is truly CLOSED; an idle gap merely ends the
// current batch.
func TestCodexEventLoop_SurvivesIdleGap(t *testing.T) {
	s := newDispatchTestSession(t)
	s.hub = NewSessionHub()
	t.Cleanup(s.hub.Stop)

	events := make(chan codexAppServerNotification, 8)
	app := &codexAppServerClient{
		pending: map[int64]chan codexAppServerResponse{},
		events:  events,
		done:    make(chan struct{}),
	}
	ctx := context.Background()
	go s.codexAppServerEventLoop(ctx, app)
	// Close the channel at teardown so the loop can exit; do NOT wait for it —
	// under the regression the goroutine may be wedged mid-cleanup and a wait
	// here would hang the test binary instead of reporting the failure.
	t.Cleanup(func() { close(events) })

	// First notification: thread/started (parsed as system/init).
	events <- codexAppServerNotification{Method: "thread/started",
		Params: json.RawMessage(`{"thread":{"id":"t-1"}}`)}

	// The idle gap. Under the bug the loop exits HERE (select default set
	// closed=true), tearing down the still-alive app-server.
	time.Sleep(200 * time.Millisecond)

	// A later notification must still be consumed by the SAME loop.
	events <- codexAppServerNotification{Method: "item/agentMessage/delta",
		Params: json.RawMessage(`{"delta":"hi"}`)}

	deadline := time.After(2 * time.Second)
	for {
		s.mu.Lock()
		seen := s.hasStreamEvents // set by handleStreamEvent — proof the delta was dispatched
		s.mu.Unlock()
		if seen {
			return // loop alive across the gap — contract holds
		}
		select {
		case <-deadline:
			t.Fatal("event loop died at the idle gap — post-gap notification never dispatched (the deployed 'exited unexpectedly' bug)")
		case <-time.After(20 * time.Millisecond):
		}
	}
}
