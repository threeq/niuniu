package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/niuniu-dev/niuniu/internal/api"
	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"

	_ "modernc.org/sqlite"
)

// openPermREST opens an in-memory SQLite DB with full schema applied.
func openPermREST(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store.Driver = "sqlite"
	if err := store.ApplySchema(db); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	store.Migrate(db)
	return db
}

// permRESTSetup creates a workspace owned by user 1 and wires a Gin engine
// with an inline middleware that injects auth_user_id and the permission routes.
func permRESTSetup(t *testing.T) (*gin.Engine, *sql.DB, int64) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := openPermREST(t)

	q := store.New(db)
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name: "ws", Path: "/tmp/ws", Status: "created",
		OwnerType: "user", OwnerID: 1,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	bus := event.NewBus()
	perm := service.NewPermissionService(db, bus, nil, time.Hour)
	authz := service.NewAuthz(q, db)
	handler := &api.PermissionHandler{
		DB:    store.Wrap(db),
		Perm:  perm,
		Authz: authz,
	}

	r := gin.New()
	// The harness keys auth as "auth_user_id" (auth/middleware.go and identity_resolver.go).
	// In tests we read X-Test-User-ID and inject it so we can vary the identity per request.
	r.Use(func(c *gin.Context) {
		uid := int64(1)
		if v := c.GetHeader("X-Test-User-ID"); v != "" {
			parsed, err := strconv.ParseInt(v, 10, 64)
			if err == nil {
				uid = parsed
			}
		}
		c.Set("auth_user_id", uid)
		c.Next()
	})
	r.GET("/api/workspaces/:id/permission-requests", handler.ListByWorkspace)
	r.POST("/api/agent-permission-decisions/:requestID", handler.Decide)
	r.GET("/api/workspaces/:id/permission-allowlist", handler.ListAllowlist)
	r.DELETE("/api/permission-allowlist/:entryID", handler.DeleteAllowlist)
	return r, db, ws.ID
}

// seedPendingRequest inserts a pending permission_request row owned by user 1
// in workspace wsID and returns its id.
func seedPendingRequest(t *testing.T, db *sql.DB, wsID int64, toolName string) int64 {
	t.Helper()
	q := store.New(db)
	id, err := q.InsertPermissionRequest(context.Background(), store.InsertPermissionRequestParams{
		WorkspaceID: wsID,
		OwnerType:   "user",
		OwnerID:     1,
		SessionID:   "sess",
		ToolName:    toolName,
		ToolInput:   `{"command":"echo hi"}`,
		ExpiresAt:   time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("InsertPermissionRequest: %v", err)
	}
	return id
}

func TestREST_ListPermissionRequests(t *testing.T) {
	r, db, wsID := permRESTSetup(t)
	seedPendingRequest(t, db, wsID, "Bash")
	seedPendingRequest(t, db, wsID, "Bash")

	req := httptest.NewRequest(http.MethodGet, "/api/workspaces/"+strconv.FormatInt(wsID, 10)+"/permission-requests", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Requests []map[string]any `json:"requests"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}
	if len(resp.Requests) != 2 {
		t.Errorf("got %d requests want 2; body=%s", len(resp.Requests), w.Body.String())
	}
}

func TestREST_DecidePermission_Allow(t *testing.T) {
	r, db, wsID := permRESTSetup(t)
	reqID := seedPendingRequest(t, db, wsID, "Bash")

	body, _ := json.Marshal(map[string]any{
		"decision": "allow",
		"scope":    "once",
	})
	httpReq := httptest.NewRequest(http.MethodPost,
		"/api/agent-permission-decisions/"+strconv.FormatInt(reqID, 10),
		bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httpReq)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var status string
	row := db.QueryRowContext(context.Background(),
		`SELECT status FROM agent_permission_requests WHERE id=?`, reqID)
	if err := row.Scan(&status); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if status != "allowed" {
		t.Errorf("got status=%q want allowed", status)
	}
}

func TestREST_DecidePermission_AlwaysHighRiskNoMatcher_422(t *testing.T) {
	r, db, wsID := permRESTSetup(t)
	reqID := seedPendingRequest(t, db, wsID, "Bash")

	body, _ := json.Marshal(map[string]any{
		"decision": "allow",
		"scope":    "always",
		"matcher": map[string]any{
			"kind":  "any",
			"value": "",
		},
	})
	httpReq := httptest.NewRequest(http.MethodPost,
		"/api/agent-permission-decisions/"+strconv.FormatInt(reqID, 10),
		bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httpReq)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d (want 422) body=%s", w.Code, w.Body.String())
	}
}

func TestREST_DecidePermission_AlreadyDecided_409(t *testing.T) {
	r, db, wsID := permRESTSetup(t)
	reqID := seedPendingRequest(t, db, wsID, "Bash")

	// Flip the row to allowed directly to simulate a prior decision.
	if _, err := db.ExecContext(context.Background(),
		`UPDATE agent_permission_requests SET status='allowed', decided_at=CURRENT_TIMESTAMP WHERE id=?`, reqID); err != nil {
		t.Fatalf("update: %v", err)
	}

	body, _ := json.Marshal(map[string]any{
		"decision": "deny",
	})
	httpReq := httptest.NewRequest(http.MethodPost,
		"/api/agent-permission-decisions/"+strconv.FormatInt(reqID, 10),
		bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httpReq)

	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d (want 409) body=%s", w.Code, w.Body.String())
	}
}

func TestREST_ListAllowlist(t *testing.T) {
	r, db, wsID := permRESTSetup(t)
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO agent_permission_allowlist (workspace_id, tool_name, matcher_kind, matcher_value, created_by)
		 VALUES (?, 'Bash', 'prefix', 'npm test', 1)`, wsID); err != nil {
		t.Fatalf("seed allowlist: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet,
		"/api/workspaces/"+strconv.FormatInt(wsID, 10)+"/permission-allowlist", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("npm test")) {
		t.Errorf("expected body to contain 'npm test'; got %s", w.Body.String())
	}
}

func TestREST_DeleteAllowlist(t *testing.T) {
	r, db, wsID := permRESTSetup(t)
	res, err := db.ExecContext(context.Background(),
		`INSERT INTO agent_permission_allowlist (workspace_id, tool_name, matcher_kind, matcher_value, created_by)
		 VALUES (?, 'Bash', 'prefix', 'npm test', 1)`, wsID)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	entryID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete,
		"/api/permission-allowlist/"+strconv.FormatInt(entryID, 10), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d (want 204) body=%s", w.Code, w.Body.String())
	}
	var n int
	_ = db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM agent_permission_allowlist WHERE id=?`, entryID).Scan(&n)
	if n != 0 {
		t.Errorf("row not deleted: count=%d", n)
	}
}

func TestREST_DeleteAllowlist_OtherWorkspace_403(t *testing.T) {
	r, db, _ := permRESTSetup(t)

	// Create a SECOND workspace owned by user 2, and an allowlist entry on it.
	q := store.New(db)
	ws2, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name: "ws-other", Path: "/tmp/ws-other", Status: "created",
		OwnerType: "user", OwnerID: 2,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace user 2: %v", err)
	}
	res, err := db.ExecContext(context.Background(),
		`INSERT INTO agent_permission_allowlist (workspace_id, tool_name, matcher_kind, matcher_value, created_by)
		 VALUES (?, 'Bash', 'prefix', 'npm run', 2)`, ws2.ID)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	entryID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}

	// User 1 (default identity) tries to delete user 2's allowlist entry.
	req := httptest.NewRequest(http.MethodDelete,
		"/api/permission-allowlist/"+strconv.FormatInt(entryID, 10), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
		t.Fatalf("status=%d (want 403 or 404) body=%s", w.Code, w.Body.String())
	}
	// Row should still exist.
	var n int
	_ = db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM agent_permission_allowlist WHERE id=?`, entryID).Scan(&n)
	if n != 1 {
		t.Errorf("row should still exist; count=%d", n)
	}
}
