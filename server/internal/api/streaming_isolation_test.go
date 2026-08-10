package api_test

// TestStreamingIsolation validates that streaming handshake authz gates work
// correctly for the four streaming endpoints. Tests are structured to cancel
// HTTP requests before srv.Close() so streaming connections are cleanly torn down.
//
// Coverage per endpoint:
//   - /ws/sse          — subscribe-time gate (cross-org workspace dropped) and
//                        per-event gate (events for dropped workspace filtered)
//   - /ws/notify       — subscribe-time identity gate (anon user → 403)
//   - /ws/workspaces/:id/terminal — subscribe-time gate (HTTP 403 before upgrade)
//   - /api/events/stream — per-event gate (cross-org events dropped, global pass)

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/agentproxy"
	"github.com/niuniu-dev/niuniu/internal/api"
	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/notify"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	_ "modernc.org/sqlite"
)

// streamingIsolationEnv holds two users each with one workspace in their own
// personal space:
//
//	UserA → WorkspaceA
//	UserB → WorkspaceB
type streamingIsolationEnv struct {
	DB         *sql.DB
	UserA      int64
	UserB      int64
	WorkspaceA int64 // owned by UserA (personal)
	WorkspaceB int64 // owned by UserB (personal)
	Authz      *service.Authz
}

func newStreamingIsolationEnv(t *testing.T) *streamingIsolationEnv {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatal(err)
	}
	store.Migrate(db)

	execID := func(q string, args ...any) int64 {
		r, err := db.Exec(q, args...)
		if err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
		id, _ := r.LastInsertId()
		return id
	}

	env := &streamingIsolationEnv{DB: db}
	env.UserA = execID(`INSERT INTO users (username, password_hash, role) VALUES ('siso-user-a', 'x', 'admin')`)
	env.UserB = execID(`INSERT INTO users (username, password_hash, role) VALUES ('siso-user-b', 'x', 'member')`)
	env.WorkspaceA = execID(
		`INSERT INTO workspaces (name, path, owner_type, owner_id) VALUES ('ws-a', '/tmp/ws-a', 'user', ?)`,
		env.UserA)
	env.WorkspaceB = execID(
		`INSERT INTO workspaces (name, path, owner_type, owner_id) VALUES ('ws-b', '/tmp/ws-b', 'user', ?)`,
		env.UserB)
	env.Authz = service.NewAuthz(store.New(db), db)
	return env
}

// firstSSELine makes an HTTP GET (with the given context) and returns the first
// "data: …" line received from the SSE stream. Returns "" if none arrive before
// ctx is cancelled. The response body is closed when the function returns.
func firstSSELine(ctx context.Context, url string) string {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	lineCh := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "data: ") {
				select {
				case lineCh <- sc.Text():
				default:
				}
				return
			}
		}
	}()
	select {
	case line := <-lineCh:
		return line
	case <-ctx.Done():
		return ""
	}
}

// collectSSELines opens an SSE stream and collects data lines until n lines are
// collected or ctx is cancelled. The caller is responsible for cancelling ctx to
// close the connection cleanly before calling srv.Close().
func collectSSELines(ctx context.Context, url string, n int) []string {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	var lines []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "data: ") {
			lines = append(lines, sc.Text())
			if len(lines) >= n {
				return lines
			}
		}
		select {
		case <-ctx.Done():
			return lines
		default:
		}
	}
	return lines
}

// ─── /ws/sse — subscribe-time gate ──────────────────────────────────────────

// TestStreamingIsolation_SSE_SubscribeGate verifies:
//  1. When ALL requested workspaces are foreign, the handler emits a "forbidden"
//     data frame immediately.
//  2. When the user requests a mix of owned and foreign workspaces, only owned
//     ones pass — connection stays open (200), no forbidden frame.
func TestStreamingIsolation_SSE_SubscribeGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	env := newStreamingIsolationEnv(t)

	proxy := agentproxy.NewAgentProxy(store.New(env.DB), nil)
	t.Cleanup(proxy.Stop)
	h := api.NewAgentProxyHandler(proxy, nil)
	h.Authz = env.Authz

	r := gin.New()
	r.GET("/ws/sse", func(c *gin.Context) {
		c.Set("auth_user_id", env.UserA)
		h.SSE(c)
	})
	srv := httptest.NewServer(r)
	// srv.Close() must not be called while SSE connections are open.
	// Each sub-test cancels its own context before returning, which closes the
	// connection so srv.Close() can proceed cleanly.
	defer srv.Close()

	t.Run("all_workspaces_rejected_sends_forbidden_frame", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		url := srv.URL + "/ws/sse?workspaces=" + fmt.Sprintf("%d", env.WorkspaceB)
		line := firstSSELine(ctx, url)
		if !strings.Contains(line, "forbidden") {
			t.Errorf("expected forbidden data frame, got: %q", line)
		}
	})

	t.Run("own_workspace_accepted_no_forbidden_frame", func(t *testing.T) {
		// Short timeout — if the connection is accepted we expect it to hang
		// open (streaming). Context cancel both asserts "no forbidden frame yet"
		// and cleanly disconnects.
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		url := srv.URL + "/ws/sse?workspaces=" + fmt.Sprintf("%d,%d", env.WorkspaceA, env.WorkspaceB)
		line := firstSSELine(ctx, url)
		// line == "" means context expired before any data frame — i.e., the
		// connection was accepted and is holding open (correct behaviour).
		if strings.Contains(line, "forbidden") {
			t.Errorf("got forbidden frame even though UserA owns WsA: %q", line)
		}
	})
}

// ─── /ws/sse — per-event gate ────────────────────────────────────────────────

// TestStreamingIsolation_SSE_PerEventGate verifies that events for workspaces
// outside the connection's authorized set are silently dropped while events for
// authorized workspaces are delivered.
func TestStreamingIsolation_SSE_PerEventGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	env := newStreamingIsolationEnv(t)

	proxy := agentproxy.NewAgentProxy(store.New(env.DB), nil)
	t.Cleanup(proxy.Stop)
	h := api.NewAgentProxyHandler(proxy, nil)
	h.Authz = env.Authz

	r := gin.New()
	r.GET("/ws/sse", func(c *gin.Context) {
		c.Set("auth_user_id", env.UserA)
		h.SSE(c)
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	// cancelable context — must be cancelled before srv.Close() via defer order.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // runs before srv.Close()

	url := srv.URL + "/ws/sse?workspaces=" + fmt.Sprintf("%d", env.WorkspaceA)

	linesCh := make(chan []string, 1)
	go func() {
		linesCh <- collectSSELines(ctx, url, 1)
	}()

	// Give the SSE handler time to register its subscription.
	time.Sleep(200 * time.Millisecond)

	hub := proxy.GetHub()
	// Cross-org event (WsB) — must be blocked by per-event gate.
	hub.Broadcast(env.WorkspaceB, event.NewOutputEvent(event.EventText, "cross-org-leak", "", "system", env.WorkspaceB))
	// Small pause, then own-workspace event — must arrive.
	time.Sleep(50 * time.Millisecond)
	hub.Broadcast(env.WorkspaceA, event.NewOutputEvent(event.EventText, "own-workspace-event", "", "system", env.WorkspaceA))

	var lines []string
	select {
	case lines = <-linesCh:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("timed out waiting for own-workspace SSE event")
	}
	cancel() // close the streaming connection

	for _, line := range lines {
		if strings.Contains(line, "cross-org-leak") {
			t.Errorf("cross-org event leaked through SSE per-event gate: %q", line)
		}
	}
	allLines := strings.Join(lines, " ")
	if !strings.Contains(allLines, "own-workspace-event") {
		t.Errorf("expected own-workspace event to be delivered, got: %v", lines)
	}
}

// TestStreamingIsolation_SSE_GlobalEventBypassesGate verifies that events whose
// WorkspaceId == 0 (global / system-scoped events that are not tied to a single
// workspace) are NOT mistaken for unauthorized cross-workspace events. The
// per-event filter in /ws/sse must pass them through without emitting a
// workspace_auth_error control frame, mirroring the same exception in
// events.go (line 49).
//
// Reproduces the production frame `data: {"type":"workspace_auth_error",
// "workspaceId":0}` observed in the browser console on team.niu6ai.com.
// The current agentproxy code only broadcasts events with
// WorkspaceId == s.workspaceID (never 0), so the in-prod source of the
// offending event isn't a current call site we could pinpoint — but the
// per-event gate must be defensively correct for any WorkspaceId=0 event
// that reaches it, exactly as events.go is.
func TestStreamingIsolation_SSE_GlobalEventBypassesGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	env := newStreamingIsolationEnv(t)

	proxy := agentproxy.NewAgentProxy(store.New(env.DB), nil)
	t.Cleanup(proxy.Stop)
	h := api.NewAgentProxyHandler(proxy, nil)
	h.Authz = env.Authz

	r := gin.New()
	r.GET("/ws/sse", func(c *gin.Context) {
		c.Set("auth_user_id", env.UserA)
		h.SSE(c)
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	url := srv.URL + "/ws/sse?workspaces=" + fmt.Sprintf("%d", env.WorkspaceA)

	linesCh := make(chan []string, 1)
	go func() {
		linesCh <- collectSSELines(ctx, url, 2)
	}()

	time.Sleep(200 * time.Millisecond)

	hub := proxy.GetHub()
	// Global event delivered via the subscribed room: hub routes by room key,
	// but the event's own WorkspaceId field is 0 (system-scoped).
	globalEv := event.NewOutputEvent(event.EventText, "global-event-marker", "", "system", 0)
	hub.Broadcast(env.WorkspaceA, globalEv)
	// Own-workspace event so collectSSELines has a definite second frame to stop on.
	time.Sleep(50 * time.Millisecond)
	hub.Broadcast(env.WorkspaceA, event.NewOutputEvent(event.EventText, "ws-event-marker", "", "system", env.WorkspaceA))

	var lines []string
	select {
	case lines = <-linesCh:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("timed out waiting for SSE events")
	}
	cancel()

	allLines := strings.Join(lines, " ")
	if strings.Contains(allLines, "workspace_auth_error") {
		t.Errorf("global event (WorkspaceId==0) wrongly triggered workspace_auth_error frame: %v", lines)
	}
	if !strings.Contains(allLines, "global-event-marker") {
		t.Errorf("global event was filtered out instead of delivered: %v", lines)
	}
}

// ─── /ws/notify — subscribe-time identity gate ───────────────────────────────

// TestStreamingIsolation_Notify_SubscribeGate verifies that the notify handler
// rejects anonymous connections (userID == 0) with HTTP 403 and accepts
// authenticated ones.
func TestStreamingIsolation_Notify_SubscribeGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	env := newStreamingIsolationEnv(t)

	hub := notify.NewNotificationHub()
	t.Cleanup(hub.Stop)
	h := api.NewNotifyHandler(hub)
	h.Authz = env.Authz

	r := gin.New()
	r.GET("/ws/notify-anon", func(c *gin.Context) {
		// auth_user_id not set → GetInt64 returns 0
		h.Connect(c)
	})
	r.GET("/ws/notify-authed", func(c *gin.Context) {
		c.Set("auth_user_id", env.UserA)
		h.Connect(c)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	t.Run("anonymous_rejected_with_403", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/ws/notify-anon")
		if err != nil {
			t.Fatalf("request error: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("expected 403 for anonymous notify connection, got %d", resp.StatusCode)
		}
	})

	t.Run("authenticated_passes_identity_gate", func(t *testing.T) {
		// The WS upgrade fails (400) because we use plain HTTP — but must NOT be 403.
		resp, err := http.Get(srv.URL + "/ws/notify-authed")
		if err != nil {
			t.Fatalf("request error: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusForbidden {
			t.Errorf("authenticated user should not get 403 from notify gate, got %d", resp.StatusCode)
		}
	})
}

// ─── /ws/workspaces/:id/terminal — subscribe-time gate ──────────────────────

// TestStreamingIsolation_Terminal_SubscribeGate verifies that the terminal
// handler returns HTTP 403 before attempting the WS upgrade when the caller
// does not own the workspace, and does NOT return 403 for their own workspace.
func TestStreamingIsolation_Terminal_SubscribeGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	env := newStreamingIsolationEnv(t)

	// AgentManager is nil; authz gate runs before any agent-manager call.
	h := api.NewAgentHandler(nil, nil)
	h.Authz = env.Authz

	r := gin.New()
	r.GET("/ws/workspaces/:id/terminal", func(c *gin.Context) {
		c.Set("auth_user_id", env.UserA)
		h.Terminal(c)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	t.Run("own_workspace_passes_gate", func(t *testing.T) {
		url := srv.URL + fmt.Sprintf("/ws/workspaces/%d/terminal", env.WorkspaceA)
		resp, err := http.Get(url)
		if err != nil {
			// A panic-recovery or EOF from the nil agentMgr is acceptable here;
			// the important assertion is that authz did NOT return 403.
			if !strings.Contains(err.Error(), "EOF") {
				t.Fatalf("unexpected request error: %v", err)
			}
			return
		}
		defer resp.Body.Close()
		// The authz gate passed; any downstream failure (500, 400) is OK — NOT 403.
		if resp.StatusCode == http.StatusForbidden {
			t.Errorf("UserA accessing own workspace should not get 403")
		}
	})

	t.Run("cross_org_workspace_gets_403", func(t *testing.T) {
		url := srv.URL + fmt.Sprintf("/ws/workspaces/%d/terminal", env.WorkspaceB)
		resp, err := http.Get(url)
		if err != nil {
			t.Fatalf("request error: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("UserA accessing UserB's workspace should get 403, got %d", resp.StatusCode)
		}
	})
}

// ─── /api/events/stream — per-event gate ────────────────────────────────────

// TestStreamingIsolation_EventsStream_PerEventGate verifies that the
// /api/events/stream handler drops cross-org workspace events, delivers
// own-workspace events, and delivers global events (WorkspaceId == 0).
func TestStreamingIsolation_EventsStream_PerEventGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	env := newStreamingIsolationEnv(t)

	bus := event.NewBus()
	defer bus.Close()
	h := api.NewEventsHandler(bus)
	h.Authz = env.Authz

	r := gin.New()
	r.GET("/api/events/stream", func(c *gin.Context) {
		c.Set("auth_user_id", env.UserA)
		h.Stream(c)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	// cancelable context — cancel before srv.Close() via defer order.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	linesCh := make(chan []string, 1)
	go func() {
		linesCh <- collectSSELines(ctx, srv.URL+"/api/events/stream", 2)
	}()

	time.Sleep(200 * time.Millisecond)

	// Cross-org event (WsB) — must be filtered.
	bus.Publish(event.OutputEvent{
		Type:        event.EventText,
		Content:     "cross-org-stream-leak",
		WorkspaceId: env.WorkspaceB,
	})
	// Own-workspace event (WsA) — must arrive.
	bus.Publish(event.OutputEvent{
		Type:        event.EventText,
		Content:     "own-stream-event",
		WorkspaceId: env.WorkspaceA,
	})
	// Global event (WorkspaceId == 0) — must arrive.
	bus.Publish(event.OutputEvent{
		Type:        event.EventHealthChanged,
		Content:     "global-stream-event",
		WorkspaceId: 0,
	})

	var lines []string
	select {
	case lines = <-linesCh:
	case <-time.After(4 * time.Second):
		cancel()
		t.Fatal("timed out waiting for events/stream events")
	}
	cancel() // close the connection

	for _, line := range lines {
		if strings.Contains(line, "cross-org-stream-leak") {
			t.Errorf("cross-org event leaked through events/stream per-event gate: %q", line)
		}
	}
	allLines := strings.Join(lines, " ")
	if !strings.Contains(allLines, "own-stream-event") {
		t.Errorf("own-workspace event not received in events/stream; got: %v", lines)
	}
	if !strings.Contains(allLines, "global-stream-event") {
		t.Errorf("global (WorkspaceId==0) event not received in events/stream; got: %v", lines)
	}
}
