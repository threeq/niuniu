package api_test

import (
	"bufio"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/api"
	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	_ "modernc.org/sqlite"
)

// TestSSE_DeliversPermissionRequest is a Task 12 integration test that proves
// publishing an OutputEvent with Type=EventPermissionRequest and a
// PermissionRequest payload through the in-process bus actually arrives at an
// SSE subscriber on /api/events/stream as a JSON-serialised wire frame.
//
// It does NOT exercise the upstream MCP handler or any DB write — those layers
// are covered by mcp_permission_test.go (Task 7) and permission_test.go. The
// only thing under test here is the bus → EventsHandler.Stream → HTTP wire
// path, including JSON tag fidelity for PermissionRequestData.
func TestSSE_DeliversPermissionRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Build a minimal in-memory DB so we can wire Authz and seed a workspace
	// owned by the test user — events.go's per-event gate would otherwise drop
	// any workspace-scoped event when Authz is non-nil and userID != 0.
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store.Driver = "sqlite"
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	store.Migrate(db)

	execID := func(q string, args ...any) int64 {
		t.Helper()
		r, err := db.Exec(q, args...)
		if err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
		id, _ := r.LastInsertId()
		return id
	}
	userID := execID(`INSERT INTO users (username, password_hash, role) VALUES ('perm-evt-user', 'x', 'admin')`)
	wsID := execID(
		`INSERT INTO workspaces (name, path, owner_type, owner_id) VALUES ('ws-perm', '/tmp/ws-perm', 'user', ?)`,
		userID)

	bus := event.NewBus()
	defer bus.Close()

	h := api.NewEventsHandler(bus)
	h.Authz = service.NewAuthz(store.New(db), db)

	r := gin.New()
	r.GET("/api/events/stream", func(c *gin.Context) {
		c.Set("auth_user_id", userID)
		h.Stream(c)
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	// Cancelable ctx so the streaming connection is torn down before srv.Close().
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type result struct {
		lines []string
		err   error
	}
	resultCh := make(chan result, 1)

	go func() {
		req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/events/stream", nil)
		if err != nil {
			resultCh <- result{err: err}
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			resultCh <- result{err: err}
			return
		}
		defer resp.Body.Close()

		var lines []string
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "data: ") {
				lines = append(lines, line)
				// The permission_request frame is the only data frame we expect;
				// returning as soon as we see it keeps the test snappy.
				if strings.Contains(line, `"type":"permission_request"`) {
					resultCh <- result{lines: lines}
					return
				}
			}
		}
		resultCh <- result{lines: lines}
	}()

	// Give the SSE handler time to register its bus subscription before we
	// publish — otherwise Publish's non-blocking send would land in nobody's
	// channel.
	time.Sleep(150 * time.Millisecond)

	now := time.Now()
	bus.Publish(event.OutputEvent{
		Type:        event.EventPermissionRequest,
		WorkspaceId: wsID,
		Ts:          now.UnixMilli(),
		PermissionRequest: &event.PermissionRequestData{
			RequestID:   7,
			SessionID:   "sess-abc",
			ToolName:    "Bash",
			ToolInput:   map[string]any{"command": "echo hi"},
			RequestedAt: now.UnixMilli(),
			ExpiresAt:   now.Add(time.Hour).UnixMilli(),
		},
	})

	var res result
	select {
	case res = <-resultCh:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("timed out waiting for permission_request SSE frame")
	}
	cancel() // close the streaming connection cleanly

	if res.err != nil {
		t.Fatalf("SSE request error: %v", res.err)
	}
	if len(res.lines) == 0 {
		t.Fatalf("no SSE data frames received")
	}

	// Find the permission_request frame and assert payload fidelity.
	var permLine string
	for _, l := range res.lines {
		if strings.Contains(l, `"type":"permission_request"`) {
			permLine = l
			break
		}
	}
	if permLine == "" {
		t.Fatalf("did not see a permission_request data frame; got: %v", res.lines)
	}
	if !strings.Contains(permLine, `"requestId":7`) {
		t.Errorf("missing requestId=7 in payload: %s", permLine)
	}
	if !strings.Contains(permLine, `"toolName":"Bash"`) {
		t.Errorf("missing toolName=Bash in payload: %s", permLine)
	}
	// Confirm the permissionRequest sub-object is present (camelCase JSON tag).
	if !strings.Contains(permLine, `"permissionRequest"`) {
		t.Errorf("missing permissionRequest sub-object in payload: %s", permLine)
	}
}
