package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"database/sql"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/api"
	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"

	_ "modernc.org/sqlite"
)

// openMCPPermTestDB mirrors the helper in service/permission_test.go: a raw
// in-memory SQLite DB with the full schema and migrations applied.
func openMCPPermTestDB(t *testing.T) *sql.DB {
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

// mintMCPToken creates a session token row directly (replicating
// MCPSessionService.Create) and returns the raw token string.
func mintMCPToken(t *testing.T, db *sql.DB, workspaceID int64) string {
	t.Helper()
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	rawStr := base64.RawURLEncoding.EncodeToString(raw[:])
	hash := sha256.Sum256([]byte(rawStr))
	hashStr := base64.RawURLEncoding.EncodeToString(hash[:])
	q := store.New(db)
	if err := q.CreateMCPSessionToken(context.Background(), store.CreateMCPSessionTokenParams{
		WorkspaceID: workspaceID,
		TokenHash:   hashStr,
		ExpiresAt:   time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("CreateMCPSessionToken: %v", err)
	}
	return rawStr
}

// TestMCPPermissionPrompt verifies the /mcp/permission-prompt route:
// - LocalhostOnly + MCPTokenAuth gate the request,
// - body {tool_name, tool_input} is delegated to PermissionService.Request,
// - an allowlist row short-circuits to {"behavior":"allow"}.
func TestMCPPermissionPrompt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openMCPPermTestDB(t)

	// Seed workspace with current_session_user_id so MCPTokenAuth can set
	// auth_user_id (not strictly required, but matches production wiring).
	q := store.New(db)
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name: "ws", Path: "/tmp/ws", Status: "created",
		OwnerType: "user", OwnerID: 1,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if _, err := db.ExecContext(context.Background(),
		`UPDATE workspaces SET current_session_user_id=? WHERE id=?`, 1, ws.ID); err != nil {
		t.Fatalf("set current_session_user_id: %v", err)
	}

	// Allowlist row that matches any Bash invocation.
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO agent_permission_allowlist (workspace_id, tool_name, matcher_kind, matcher_value, created_by)
		 VALUES (?, ?, 'any', '', 1)`, ws.ID, "Bash"); err != nil {
		t.Fatalf("seed allowlist: %v", err)
	}

	bus := event.NewBus()
	perm := service.NewPermissionService(db, bus, nil, time.Hour)
	mcpSess := service.NewMCPSessionService(q)
	handler := &api.MCPPermissionHandler{Perm: perm, Q: q}

	r := gin.New()
	mcpGroup := r.Group("/mcp")
	mcpGroup.Use(api.LocalhostOnly())
	mcpGroup.Use(api.MCPTokenAuth(mcpSess, store.New(db)))
	mcpGroup.POST("/permission-prompt", handler.Prompt)

	token := mintMCPToken(t, db, ws.ID)

	body, _ := json.Marshal(map[string]any{
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "echo hi"},
	})
	req := httptest.NewRequest(http.MethodPost, "/mcp/permission-prompt", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.RemoteAddr = "127.0.0.1:12345"

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp service.AllowResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}
	if resp.Behavior != "allow" {
		t.Errorf("got behavior=%q want allow; body=%s", resp.Behavior, w.Body.String())
	}
}
