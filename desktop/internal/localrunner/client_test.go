package localrunner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// collected accumulates the frames the fake server received for one request.
type collected struct {
	logs    []logFrame
	resp    responseFrame
	gotExit bool
}

// roundTrip stands up a fake server that sends exactly one request frame, then
// runs a real Client against it and returns the frames the server got back. cfg
// supplies the Gateway/Syncer; BaseURL + WorkspaceID are filled in.
func roundTrip(t *testing.T, cfg Config, req requestFrame) collected {
	t.Helper()
	resultCh := make(chan collected, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The Go client sends no Origin header, so default Accept (which only
		// checks Origin when present) admits it without any skip-verify.
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conn.SetReadLimit(-1)
		defer conn.Close(websocket.StatusNormalClosure, "")

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		frame, _ := json.Marshal(req)
		if err := conn.Write(ctx, websocket.MessageText, frame); err != nil {
			return
		}

		var col collected
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var probe struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(data, &probe) != nil {
				continue
			}
			switch probe.Type {
			case "log":
				var lf logFrame
				_ = json.Unmarshal(data, &lf)
				col.logs = append(col.logs, lf)
			case "exit":
				col.gotExit = true
			case "response":
				_ = json.Unmarshal(data, &col.resp)
				resultCh <- col
				return
			}
		}
	}))
	defer srv.Close()

	cfg.BaseURL = srv.URL
	cfg.WorkspaceID = "1"
	client := New(cfg)
	client.Start()
	defer client.Stop()

	select {
	case col := <-resultCh:
		return col
	case <-time.After(8 * time.Second):
		t.Fatal("timed out waiting for runner response")
		return collected{}
	}
}

func TestClient_ExecFlow(t *testing.T) {
	dir := t.TempDir()
	gw := NewGateway(GatewayConfig{Dir: dir, Allowed: []string{"echo"}, Audit: &memAuditor{}})

	col := roundTrip(t, Config{Gateway: gw}, requestFrame{
		Type: "request", ID: "req-1", Kind: kindExec, Command: echoCmd("hi"),
	})

	if !col.resp.OK || col.resp.Exit != 0 {
		t.Fatalf("expected clean exec, got %+v", col.resp)
	}
	if col.resp.ID != "req-1" {
		t.Fatalf("response id = %q, want req-1", col.resp.ID)
	}
	if !strings.Contains(col.resp.Stdout, "hi") {
		t.Fatalf("stdout %q should contain 'hi'", col.resp.Stdout)
	}
	if !col.gotExit {
		t.Fatal("expected an exit frame before the response")
	}
	sawStdoutLog := false
	for _, l := range col.logs {
		if l.Level == levelStdout && strings.Contains(l.Text, "hi") {
			sawStdoutLog = true
		}
	}
	if !sawStdoutLog {
		t.Fatalf("expected a streamed stdout log frame, got %+v", col.logs)
	}
}

func TestClient_ExecDenied(t *testing.T) {
	// Empty whitelist + no approver ⇒ fail-safe deny; the command must NOT run.
	gw := NewGateway(GatewayConfig{Dir: t.TempDir(), Audit: &memAuditor{}})

	col := roundTrip(t, Config{Gateway: gw}, requestFrame{
		Type: "request", ID: "req-2", Kind: kindExec, Command: "rm -rf /",
	})

	if col.resp.OK {
		t.Fatal("denied command must report ok=false")
	}
	if col.resp.Error == "" {
		t.Fatal("denied command should carry a reason")
	}
}

func TestClient_ReadFlow(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("file body"), 0o644); err != nil {
		t.Fatal(err)
	}
	gw := NewGateway(GatewayConfig{Dir: dir, Audit: &memAuditor{}})

	col := roundTrip(t, Config{Gateway: gw}, requestFrame{
		Type: "request", ID: "req-3", Kind: kindRead, Path: "hello.txt",
	})

	if !col.resp.OK {
		t.Fatalf("read should succeed: %+v", col.resp)
	}
	if col.resp.Content != "file body" {
		t.Fatalf("content = %q", col.resp.Content)
	}
}

func TestClient_ReadRejectsEscape(t *testing.T) {
	gw := NewGateway(GatewayConfig{Dir: t.TempDir(), Audit: &memAuditor{}})
	col := roundTrip(t, Config{Gateway: gw}, requestFrame{
		Type: "request", ID: "req-4", Kind: kindRead, Path: "../../etc/passwd",
	})
	if col.resp.OK {
		t.Fatal("path escape must be rejected")
	}
}

func TestClient_UnknownKind(t *testing.T) {
	gw := NewGateway(GatewayConfig{Dir: t.TempDir(), Audit: &memAuditor{}})
	col := roundTrip(t, Config{Gateway: gw}, requestFrame{
		Type: "request", ID: "req-5", Kind: "bogus",
	})
	if col.resp.OK || !strings.Contains(col.resp.Error, "unknown request kind") {
		t.Fatalf("unknown kind should error, got %+v", col.resp)
	}
}

func TestClient_SyncFlow(t *testing.T) {
	dir := t.TempDir()
	gw := NewGateway(GatewayConfig{Dir: dir, Audit: &memAuditor{}})
	syncer := newTestSyncer(dir, &fakeProvider{states: []RepoState{{CurrentBranch: "main"}}}, (&recGit{}).run)

	col := roundTrip(t, Config{Gateway: gw, Syncer: syncer}, requestFrame{
		Type: "request", ID: "req-6", Kind: kindSync,
	})
	if !col.resp.OK {
		t.Fatalf("sync should succeed: %+v", col.resp)
	}
	if !strings.Contains(col.resp.Content, "checked out main") {
		t.Fatalf("sync summary = %q", col.resp.Content)
	}
}

func TestJittered_StaysWithinBounds(t *testing.T) {
	const base = 4 * time.Second
	lo := time.Duration(float64(base) * (1 - jitterFraction))
	hi := time.Duration(float64(base) * (1 + jitterFraction))
	sawBelowBase, sawAboveBase := false, false
	for i := 0; i < 2000; i++ {
		got := jittered(base)
		if got < lo || got >= hi {
			t.Fatalf("jittered(%v) = %v, out of [%v, %v)", base, got, lo, hi)
		}
		if got < base {
			sawBelowBase = true
		}
		if got > base {
			sawAboveBase = true
		}
	}
	// It must actually spread both directions, not collapse to a constant.
	if !sawBelowBase || !sawAboveBase {
		t.Fatalf("jitter did not spread both sides: below=%v above=%v", sawBelowBase, sawAboveBase)
	}
}

func TestClient_StopIsClean(t *testing.T) {
	// Dial a dead address; Start/Stop must not hang or panic.
	gw := NewGateway(GatewayConfig{Dir: t.TempDir(), Audit: &memAuditor{}})
	client := New(Config{BaseURL: "http://127.0.0.1:0", WorkspaceID: "1", Gateway: gw})
	client.Start()
	time.Sleep(50 * time.Millisecond)
	done := make(chan struct{})
	go func() { client.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop() hung")
	}
}
