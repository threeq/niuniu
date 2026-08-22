package service

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
)

// seedDeletableTask creates a project/column/issue plus a no-repo workspace
// backed by that issue (cleanup = plain directory removal, deterministic in
// tests) and returns the ids. It shares the WorkspaceService's DB so the
// DispatchService under test sees the same rows.
func seedDeletableTask(t *testing.T, ctx context.Context, q *store.Queries, ws *WorkspaceService, title string) (projectID, issueID, wsID int64) {
	t.Helper()
	proj, err := q.CreateProject(ctx, store.CreateProjectParams{Name: title + "-proj", OwnerType: "user", OwnerID: 1})
	require.NoError(t, err)
	col, err := q.CreateColumn(ctx, store.CreateColumnParams{ProjectID: proj.ID, Name: "待办", Position: 0})
	require.NoError(t, err)
	issue, err := q.CreateIssue(ctx, store.CreateIssueParams{ColumnID: col.ID, Title: title, Position: 0})
	require.NoError(t, err)
	res, err := ws.Create(ctx, CreateWorkspaceInput{IssueID: &issue.ID, Name: title, OwnerType: "user", OwnerID: 1, NoRepo: true})
	require.NoError(t, err)
	return proj.ID, issue.ID, res.Workspace.ID
}

// TestAssistantDispatch_DeleteTask is the end-to-end cascade: deleting a task
// removes both its issue and its backing workspace, and fires the stop hook for
// the workspace's agent session first.
func TestAssistantDispatch_DeleteTask(t *testing.T) {
	ctx := context.Background()
	wsSvc, db := newWorkspaceServiceForTest(t)
	db.SetMaxOpenConns(1)
	q := store.New(db)
	kanban := NewKanbanService(db, q, nil, nil, nil)
	disp := NewDispatchService(kanban, wsSvc, q, nil)

	projectID, issueID, wsID := seedDeletableTask(t, ctx, q, wsSvc, "任务")

	var stopped []int64
	stop := func(_ context.Context, id int64) { stopped = append(stopped, id) }

	require.NoError(t, disp.DeleteTask(ctx, projectID, issueID, stop))

	_, err := q.GetIssue(ctx, issueID)
	require.Error(t, err, "issue should be deleted")
	_, err = q.GetWorkspace(ctx, wsID)
	require.Error(t, err, "workspace should be deleted")
	require.Equal(t, []int64{wsID}, stopped, "stop hook must fire for the workspace before cleanup")
}

// TestAssistantDispatch_DeleteTask_CrossProjectRejected guards the shared-bot
// isolation: a project may only delete its own tasks. An issue from another
// project is rejected (ErrTaskNotInProject) and left intact.
func TestAssistantDispatch_DeleteTask_CrossProjectRejected(t *testing.T) {
	ctx := context.Background()
	wsSvc, db := newWorkspaceServiceForTest(t)
	db.SetMaxOpenConns(1)
	q := store.New(db)
	kanban := NewKanbanService(db, q, nil, nil, nil)
	disp := NewDispatchService(kanban, wsSvc, q, nil)

	_, issueID, wsID := seedDeletableTask(t, ctx, q, wsSvc, "任务")
	otherProj, err := q.CreateProject(ctx, store.CreateProjectParams{Name: "other", OwnerType: "user", OwnerID: 1})
	require.NoError(t, err)

	err = disp.DeleteTask(ctx, otherProj.ID, issueID, nil)
	require.ErrorIs(t, err, ErrTaskNotInProject)

	// Both the issue and its workspace survive the rejected delete.
	_, err = q.GetIssue(ctx, issueID)
	require.NoError(t, err)
	_, err = q.GetWorkspace(ctx, wsID)
	require.NoError(t, err)
}
