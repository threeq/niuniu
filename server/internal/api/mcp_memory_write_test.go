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

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/api"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
)

// setupMCPMemoryWrite wires the reversible memory MCP routes with the real
// MCPTokenAuth gate and seeds:
//   - memory 1: owner user 1 (the workspace's owner) — mutable
//   - memory 2: owner user 2 (cross-tenant) — must be invisible/immutable
//   - workspace 1 owned by user 1; returns its MCP session token, id, and db.
func setupMCPMemoryWrite(t *testing.T) (*gin.Engine, string, int64, *sql.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := openMCPPermTestDB(t)
	db.SetMaxOpenConns(1)
	q := store.New(db)
	ctx := context.Background()

	mustExec(t, db, `INSERT INTO memories (id, owner_type, owner_id, mem_type, title, content, source) VALUES (1,'user',1,'gotcha','m1','c1','manual')`)
	mustExec(t, db, `INSERT INTO memories (id, owner_type, owner_id, mem_type, title, content, source) VALUES (2,'user',2,'gotcha','m2','c2','manual')`)

	ws, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
		Name: "ws1", Path: "/tmp/ws1", Status: "created", OwnerType: "user", OwnerID: 1,
	})
	require.NoError(t, err)
	mustExec(t, db, `UPDATE workspaces SET current_session_user_id=1 WHERE id=?`, ws.ID)

	memSvc := service.NewMemoryService(q, db, "claude")
	authz := service.NewAuthz(q, db)
	memHandler := api.NewMemoryHandler(memSvc, authz, db, nil)
	mcpSess := service.NewMCPSessionService(q)

	r := gin.New()
	g := r.Group("/mcp")
	g.Use(api.LocalhostOnly())
	g.Use(api.MCPTokenAuth(mcpSess, store.New(db)))
	g.POST("/workspaces/:id/memory/update", memHandler.MCPUpdateMemory)
	g.POST("/workspaces/:id/memory/delete", memHandler.MCPDeleteMemory)
	g.POST("/workspaces/:id/memory/restore", memHandler.MCPRestoreMemory)

	return r, mintMCPToken(t, db, ws.ID), ws.ID, db
}

func memMCPPost(t *testing.T, r *gin.Engine, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.RemoteAddr = "127.0.0.1:9999"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func memIsDeleted(t *testing.T, db *sql.DB, id int64) bool {
	t.Helper()
	var deleted *string
	require.NoError(t, db.QueryRowContext(context.Background(),
		`SELECT deleted_at FROM memories WHERE id=?`, id).Scan(&deleted))
	return deleted != nil
}

// An agent can soft-delete then restore its OWN owner's memory; delete is
// reversible (deleted_at toggles, the row is not erased).
func TestMCPMemory_DeleteAndRestoreOwn(t *testing.T) {
	r, token, wsID, db := setupMCPMemoryWrite(t)
	base := "/mcp/workspaces/" + strconv.FormatInt(wsID, 10) + "/memory"

	w := memMCPPost(t, r, base+"/delete", token, `{"id":1}`)
	require.Equal(t, 200, w.Code, "body=%s", w.Body.String())
	require.True(t, memIsDeleted(t, db, 1), "memory 1 should be soft-deleted")

	w = memMCPPost(t, r, base+"/restore", token, `{"id":1}`)
	require.Equal(t, 200, w.Code, "body=%s", w.Body.String())
	require.False(t, memIsDeleted(t, db, 1), "memory 1 should be restored")
}

// An agent must NOT touch another owner's memory: cross-tenant id is reported as
// not-found and the row is left intact.
func TestMCPMemory_CrossTenantDenied(t *testing.T) {
	r, token, wsID, db := setupMCPMemoryWrite(t)
	base := "/mcp/workspaces/" + strconv.FormatInt(wsID, 10) + "/memory"

	w := memMCPPost(t, r, base+"/delete", token, `{"id":2}`)
	require.Equal(t, 404, w.Code, "cross-tenant delete must be 404, body=%s", w.Body.String())
	require.False(t, memIsDeleted(t, db, 2), "cross-tenant memory must NOT be deleted")
}

// Update edits the owner's memory; an omitted field keeps its prior value.
func TestMCPMemory_UpdateOwn(t *testing.T) {
	r, token, wsID, _ := setupMCPMemoryWrite(t)
	base := "/mcp/workspaces/" + strconv.FormatInt(wsID, 10) + "/memory"

	w := memMCPPost(t, r, base+"/update", token, `{"id":1,"content":"corrected"}`)
	require.Equal(t, 200, w.Code, "body=%s", w.Body.String())
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "corrected", resp["content"])
	require.Equal(t, "m1", resp["title"], "omitted title keeps its prior value")
}
