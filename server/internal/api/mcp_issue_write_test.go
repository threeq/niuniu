package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/api"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupMCPIssueWrite wires the issue write routes onto an mcpGroup with the
// real MCPTokenAuth gate, reusing the same handlers the production router
// registers. It seeds:
//   - project 1 (owner user 1) with two columns (1, 2) and issue 1 in col 1
//   - project 2 (owner user 2) with column 3 and issue 2 (cross-tenant target)
//   - workspace 1 (owner user 1) whose current_session_user_id=1
//
// Returns the engine and the raw MCP session token scoped to workspace 1.
func setupMCPIssueWrite(t *testing.T) (*gin.Engine, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db := openMCPPermTestDB(t)
	// In-memory SQLite gives each pooled connection its own private database, so
	// the schema applied on one connection is invisible to the next. Production
	// store.Open pins this to 1; do the same here so the multi-query
	// loadIssueDetail path (issue + assignees + labels + checklist) all hit the
	// one connection that holds the schema.
	db.SetMaxOpenConns(1)
	q := store.New(db)
	ctx := context.Background()

	// user-1-owned project + columns + issue
	mustExec(t, db, `INSERT INTO projects (id, name, owner_type, owner_id) VALUES (1, 'p1', 'user', 1)`)
	mustExec(t, db, `INSERT INTO columns (id, project_id, name, position) VALUES (1, 1, 'todo', 0)`)
	mustExec(t, db, `INSERT INTO columns (id, project_id, name, position) VALUES (2, 1, 'done', 1)`)
	mustExec(t, db, `INSERT INTO issues (id, column_id, title, description, priority, position) VALUES (1, 1, 'orig title', 'orig desc', 1, 0)`)

	// user-2-owned project + column + issue (cross-tenant target)
	mustExec(t, db, `INSERT INTO projects (id, name, owner_type, owner_id) VALUES (2, 'p2', 'user', 2)`)
	mustExec(t, db, `INSERT INTO columns (id, project_id, name, position) VALUES (3, 2, 'todo', 0)`)
	mustExec(t, db, `INSERT INTO issues (id, column_id, title, position) VALUES (2, 3, 'other tenant', 0)`)

	ws, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
		Name: "ws1", Path: "/tmp/ws1", Status: "created", OwnerType: "user", OwnerID: 1,
	})
	require.NoError(t, err)
	mustExec(t, db, `UPDATE workspaces SET current_session_user_id=1 WHERE id=?`, ws.ID)

	activitySvc := service.NewIssueActivityService(q)
	kanbanSvc := service.NewKanbanService(db, q, activitySvc, nil, nil)
	checklistSvc := service.NewIssueChecklistService(q)
	authz := service.NewAuthz(q, db)

	kanbanHandler := api.NewKanbanHandler(kanbanSvc, checklistSvc)
	kanbanHandler.Authz = authz
	issueHandler := api.NewIssueHandler(kanbanSvc, authz)
	checklistHandler := api.NewIssueChecklistHandler(checklistSvc)
	checklistHandler.Authz = authz

	mcpSess := service.NewMCPSessionService(q)

	r := gin.New()
	g := r.Group("/mcp")
	g.Use(api.LocalhostOnly())
	g.Use(api.MCPTokenAuth(mcpSess, store.New(db)))
	g.GET("/issues/:id", kanbanHandler.GetIssue)
	g.GET("/issues/:id/checklists", checklistHandler.List)
	g.PUT("/issues/:id", kanbanHandler.UpdateIssue)
	g.DELETE("/issues/:id", kanbanHandler.DeleteIssue)
	g.PUT("/issues/:id/move", kanbanHandler.MoveIssue)
	g.PUT("/issues/:id/labels", issueHandler.SetLabels)
	g.POST("/issues/:id/checklists", checklistHandler.Create)
	g.PUT("/checklists/:checklistId", checklistHandler.Update)

	return r, mintMCPToken(t, db, ws.ID)
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func mcpPUT(t *testing.T, r *gin.Engine, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.RemoteAddr = "127.0.0.1:9999"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func mcpDELETE(t *testing.T, r *gin.Engine, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.RemoteAddr = "127.0.0.1:9999"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestMCPUpdateIssue_Scalars verifies PUT /mcp/issues/:id updates the scalar
// fields and that the merge (full-overwrite body the tool sends) round-trips.
func TestMCPUpdateIssue_Scalars(t *testing.T) {
	r, token := setupMCPIssueWrite(t)

	body := `{"title":"new title","description":"new desc","priority":2,"start_date":"","due_date":"","estimate_type":"","estimate":0,"actual_time":0,"goal_condition":""}`
	w := mcpPUT(t, r, "/mcp/issues/1", token, body)
	require.Equal(t, 200, w.Code, "body=%s", w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "new title", resp["title"])
	assert.EqualValues(t, 2, resp["priority"])
}

// TestMCPUpdateIssue_EmptyTitleRejected documents that an empty title is
// rejected (gin binding:"required" fails on the zero value), so the
// update_issue tool can never silently blank out an issue's title — it surfaces
// a 400 instead.
func TestMCPUpdateIssue_EmptyTitleRejected(t *testing.T) {
	r, token := setupMCPIssueWrite(t)

	w := mcpPUT(t, r, "/mcp/issues/1", token,
		`{"title":"","description":"x","priority":1,"estimate":0,"actual_time":0}`)
	require.Equal(t, 400, w.Code, "empty title must be rejected, body=%s", w.Body.String())

	// Original title is untouched.
	g := mcpGET(t, r, "/mcp/issues/1", token)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(g.Body.Bytes(), &resp))
	assert.Equal(t, "orig title", resp["title"])
}

// TestMCPMoveIssue verifies PUT /mcp/issues/:id/move shifts the issue to a
// same-project column.
func TestMCPMoveIssue(t *testing.T) {
	r, token := setupMCPIssueWrite(t)

	w := mcpPUT(t, r, "/mcp/issues/1/move", token, `{"column_id":2,"position":0}`)
	require.Equal(t, 204, w.Code, "body=%s", w.Body.String())

	// Confirm via GET.
	g := mcpGET(t, r, "/mcp/issues/1", token)
	require.Equal(t, 200, g.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(g.Body.Bytes(), &resp))
	assert.EqualValues(t, 2, resp["column_id"])
}

// TestMCPChecklistAddAndToggle verifies add (POST) + toggle (PUT) on the
// checklist routes.
func TestMCPChecklistAddAndToggle(t *testing.T) {
	r, token := setupMCPIssueWrite(t)

	w := mcpPOST(t, r, "/mcp/issues/1/checklists", token, `{"title":"step one"}`)
	require.Equal(t, 201, w.Code, "body=%s", w.Body.String())
	var item map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &item))
	idF, ok := item["id"].(float64)
	require.True(t, ok, "checklist id missing: %s", w.Body.String())

	tg := mcpPUT(t, r, fmt.Sprintf("/mcp/checklists/%d", int64(idF)), token,
		`{"title":"step one","is_completed":1}`)
	require.Equal(t, 200, tg.Code, "body=%s", tg.Body.String())
	var toggled map[string]any
	require.NoError(t, json.Unmarshal(tg.Body.Bytes(), &toggled))
	assert.EqualValues(t, 1, toggled["is_completed"])
}

// TestMCPDeleteIssue verifies DELETE /mcp/issues/:id removes the issue.
func TestMCPDeleteIssue(t *testing.T) {
	r, token := setupMCPIssueWrite(t)

	w := mcpDELETE(t, r, "/mcp/issues/1", token)
	require.Equal(t, 204, w.Code, "body=%s", w.Body.String())

	g := mcpGET(t, r, "/mcp/issues/1", token)
	require.Equal(t, 404, g.Code, "deleted issue should 404, body=%s", g.Body.String())
}

// TestMCPIssueWrite_CrossTenant verifies a session scoped to user 1 cannot
// update or delete a user-2-owned issue — the CanAccessIssue gate returns 403.
func TestMCPIssueWrite_CrossTenant(t *testing.T) {
	r, token := setupMCPIssueWrite(t)

	upd := mcpPUT(t, r, "/mcp/issues/2", token,
		`{"title":"hijack","description":"","priority":0,"estimate":0,"actual_time":0}`)
	assert.Equal(t, 403, upd.Code, "cross-tenant update must be forbidden, body=%s", upd.Body.String())

	del := mcpDELETE(t, r, "/mcp/issues/2", token)
	assert.Equal(t, 403, del.Code, "cross-tenant delete must be forbidden, body=%s", del.Body.String())
}
