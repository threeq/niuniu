package api_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/store"
	testutil "github.com/niuniu-dev/niuniu/internal/testing"
	"github.com/stretchr/testify/require"
)

// seedWorkspaceForMarkDone creates a minimal project/column/issue/workspace
// chain with the workspace pre-seeded at the given status, and returns the
// workspace ID. Mirrors setupMarkDoneTest in service/workspace_ops_test.go
// but uses the handler-level testutil.SetupTestServer DB.
func seedWorkspaceForMarkDone(t *testing.T, db *sql.DB, status string) int64 {
	t.Helper()
	ctx := context.Background()
	q := store.New(db)

	proj, err := q.CreateProject(ctx, store.CreateProjectParams{Name: "p-" + status, OwnerType: "user"})
	require.NoError(t, err)
	col, err := q.CreateColumn(ctx, store.CreateColumnParams{ProjectID: proj.ID, Name: "todo", Position: 0})
	require.NoError(t, err)
	issue, err := q.CreateIssue(ctx, store.CreateIssueParams{ColumnID: col.ID, Title: "test issue", Position: 0})
	require.NoError(t, err)
	ws, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
		IssueID:   sql.NullInt64{Int64: issue.ID, Valid: true},
		Name:      "ws-" + status,
		Path:      t.TempDir(),
		Status:    status,
		OwnerType: "user",
		OwnerID:   0,
	})
	require.NoError(t, err)
	return ws.ID
}

// hasErrorCode returns true if the response body is a JSON object whose
// "code" or "error.code" field equals `want`. Standard response.go envelopes
// use either flat {"code":"..."} or {"error":{"code":"..."}}.
func hasErrorCode(body []byte, want string) bool {
	var flat struct {
		Code  string `json:"code"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &flat); err != nil {
		return false
	}
	return flat.Code == want || flat.Error.Code == want
}

func TestMarkDoneHandler_RejectsRunning(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, db := testutil.SetupTestServer(t)
	wsID := seedWorkspaceForMarkDone(t, db, "running")

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/workspaces/"+strconv.FormatInt(wsID, 10)+"/mark-done", nil)
	srv.Engine().ServeHTTP(rec, req)

	require.Equal(t, http.StatusConflict, rec.Code, "body=%s", rec.Body.String())
	require.True(t, hasErrorCode(rec.Body.Bytes(), "WORKSPACE_RUNNING"),
		"response should contain WORKSPACE_RUNNING code, got %s", rec.Body.String())
}

func TestMarkDoneHandler_RejectsArchived(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, db := testutil.SetupTestServer(t)
	// Seed a non-running workspace, then archive it so CheckNotArchived trips.
	wsID := seedWorkspaceForMarkDone(t, db, "needs_review")
	q := store.New(db)
	require.NoError(t, q.ArchiveWorkspace(context.Background(), wsID))

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/workspaces/"+strconv.FormatInt(wsID, 10)+"/mark-done", nil)
	srv.Engine().ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
	require.True(t, hasErrorCode(rec.Body.Bytes(), "WORKSPACE_ARCHIVED"),
		"response should contain WORKSPACE_ARCHIVED code, got %s", rec.Body.String())
}

func TestMarkDoneHandler_SucceedsOnNeedsReview(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, db := testutil.SetupTestServer(t)
	wsID := seedWorkspaceForMarkDone(t, db, "needs_review")

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/workspaces/"+strconv.FormatInt(wsID, 10)+"/mark-done", nil)
	srv.Engine().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	// Happy path: response must NOT contain warnings field.
	var resp map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "body=%s", rec.Body.String())
	if _, ok := resp["warnings"]; ok {
		t.Errorf("expected no warnings on happy path, got body=%s", rec.Body.String())
	}

	q := store.New(db)
	ws, err := q.GetWorkspace(context.Background(), wsID)
	require.NoError(t, err)
	require.Equal(t, "completed", ws.Status,
		"workspace.status = %q, want completed", ws.Status)
}

// TestMarkDoneHandler_ReturnsWarningsOnPartialSuccess verifies the
// HTTP-level partial-success contract: when service.MarkWorkspaceDone
// returns warnings, the handler must include them in the response body
// (still 200) so the SPA can render toast.warning instead of toast.success.
//
// Same dangling-issue construction as
// TestMarkWorkspaceDone_PartialSuccessOnLifecycleFail in the service test.
func TestMarkDoneHandler_ReturnsWarningsOnPartialSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, db := testutil.SetupTestServer(t)
	ctx := context.Background()
	wsID := seedWorkspaceForMarkDone(t, db, "needs_review")

	// Find the issue this workspace points at and delete it with FKs off
	// so workspaces.issue_id is left dangling (ON DELETE SET NULL would
	// otherwise null it out and skip the lifecycle call entirely).
	q := store.New(db)
	ws, err := q.GetWorkspace(ctx, wsID)
	require.NoError(t, err)
	require.True(t, ws.IssueID.Valid, "seeded workspace must have an issue_id")
	issueID := ws.IssueID.Int64

	_, err = db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `DELETE FROM issues WHERE id = ?`, issueID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `PRAGMA foreign_keys = ON`)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/workspaces/"+strconv.FormatInt(wsID, 10)+"/mark-done", nil)
	srv.Engine().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	var resp struct {
		Status   string   `json:"status"`
		Warnings []string `json:"warnings"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "body=%s", rec.Body.String())
	require.Equal(t, "ok", resp.Status)
	require.Equal(t, []string{"issue_lifecycle_sync_failed"}, resp.Warnings,
		"expected warnings=[issue_lifecycle_sync_failed], got body=%s", rec.Body.String())

	ws, err = q.GetWorkspace(ctx, wsID)
	require.NoError(t, err)
	require.Equal(t, "completed", ws.Status,
		"workspace.status = %q, want completed", ws.Status)
}
