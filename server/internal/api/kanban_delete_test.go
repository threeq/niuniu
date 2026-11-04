package api_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/store"
	testutil "github.com/niuniu-dev/niuniu/internal/testing"
	"github.com/stretchr/testify/require"
)

// TestDeleteIssue_DeletesWorkspace: deleting an issue tears down its workspace
// (same semantics as DELETE /workspaces/:id). A workspace with no worktree has
// no uncommitted changes, so the guard admits the delete.
func TestDeleteIssue_DeletesWorkspace(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, db := testutil.SetupTestServer(t)
	q := store.New(db)
	ctx := context.Background()

	proj, err := q.CreateProject(ctx, store.CreateProjectParams{Name: "p-del", OwnerType: "user", OwnerID: 1})
	require.NoError(t, err)
	col, err := q.CreateColumn(ctx, store.CreateColumnParams{ProjectID: proj.ID, Name: "todo", Position: 0})
	require.NoError(t, err)
	issue, err := q.CreateIssue(ctx, store.CreateIssueParams{ColumnID: col.ID, Title: "to delete", Position: 0})
	require.NoError(t, err)
	ws, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
		IssueID:   sql.NullInt64{Int64: issue.ID, Valid: true},
		Name:      "ws",
		Path:      t.TempDir(),
		Status:    "created",
		OwnerType: "user",
		OwnerID:   1,
	})
	require.NoError(t, err)

	req, _ := http.NewRequest("DELETE", "/api/issues/"+strconv.FormatInt(issue.ID, 10), nil)
	w := httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, req)
	require.Equal(t, http.StatusNoContent, w.Code, "body=%s", w.Body.String())

	// Workspace row is gone.
	_, err = q.GetWorkspace(ctx, ws.ID)
	require.ErrorIs(t, err, sql.ErrNoRows, "workspace should be deleted with its issue")
	// Issue row is gone too.
	_, err = q.GetIssue(ctx, issue.ID)
	require.Error(t, err)
}
