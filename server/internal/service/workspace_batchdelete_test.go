package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
)

// newBatchDeleteWorkspace creates a no-repo workspace and returns its id. No-repo
// keeps cleanup to a plain directory removal so the async path is deterministic
// in tests (no git worktree plumbing required).
func newBatchDeleteWorkspace(t *testing.T, svc *WorkspaceService, name string) int64 {
	t.Helper()
	res, err := svc.Create(context.Background(), CreateWorkspaceInput{
		Name:      name,
		OwnerType: "user",
		OwnerID:   1,
		NoRepo:    true,
	})
	require.NoError(t, err)
	return res.Workspace.ID
}

// TestBatchDelete_NotFound verifies a non-existent id is skipped (not_found) and
// never accepted.
func TestBatchDelete_NotFound(t *testing.T) {
	svc, db := newWorkspaceServiceForTest(t)
	// Pin to one connection: openWorkspaceTestDB uses a bare :memory: DSN, so a
	// second pooled connection would be a separate empty database. The async
	// cleanup goroutine must see the same schema-bearing connection.
	db.SetMaxOpenConns(1)

	res := svc.BatchDelete(context.Background(), []int64{999999}, true, nil)

	require.Empty(t, res.Accepted)
	require.Len(t, res.Skipped, 1)
	require.Equal(t, int64(999999), res.Skipped[0].ID)
	require.Equal(t, "not_found", res.Skipped[0].Reason)
}

// TestBatchDelete_AlreadyDeleting is the core dedup guarantee: a workspace whose
// status is already 'deleting' must be skipped (already_deleting), not deleted a
// second time. This proves "不能重复提交或删除" at the store level — the atomic
// MarkWorkspaceDeleting returns 0 rows and no cleanup goroutine is spawned.
func TestBatchDelete_AlreadyDeleting(t *testing.T) {
	ctx := context.Background()
	svc, db := newWorkspaceServiceForTest(t)
	db.SetMaxOpenConns(1)
	q := store.New(db)

	id := newBatchDeleteWorkspace(t, svc, "ws-already-deleting")
	require.NoError(t, q.UpdateWorkspaceStatus(ctx, store.UpdateWorkspaceStatusParams{Status: "deleting", ID: id}))

	res := svc.BatchDelete(ctx, []int64{id}, true, nil)

	require.Empty(t, res.Accepted)
	require.Len(t, res.Skipped, 1)
	require.Equal(t, id, res.Skipped[0].ID)
	require.Equal(t, "already_deleting", res.Skipped[0].Reason)

	// The workspace must still exist and remain in 'deleting' — never re-deleted.
	ws, err := q.GetWorkspace(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "deleting", ws.Status)
}

// TestBatchDelete_Accepted verifies the happy path: the workspace is accepted,
// the stop hook fires before cleanup, and the row is asynchronously removed.
func TestBatchDelete_Accepted(t *testing.T) {
	ctx := context.Background()
	svc, db := newWorkspaceServiceForTest(t)
	db.SetMaxOpenConns(1)
	q := store.New(db)

	id := newBatchDeleteWorkspace(t, svc, "ws-accepted")

	var mu sync.Mutex
	var stopped []int64
	stop := func(_ context.Context, wid int64) {
		mu.Lock()
		stopped = append(stopped, wid)
		mu.Unlock()
	}

	res := svc.BatchDelete(ctx, []int64{id}, true, stop)

	require.Equal(t, []int64{id}, res.Accepted)
	require.Empty(t, res.Skipped)

	// Async cleanup removes the row; poll until gone. The window is generous
	// because removeDirectoryWithProcessCleanup can be slow on Windows under load
	// (process enumeration + retries) — the deletion still completes.
	require.Eventually(t, func() bool {
		_, err := q.GetWorkspace(ctx, id)
		return err != nil
	}, 30*time.Second, 50*time.Millisecond, "workspace should be deleted asynchronously")

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []int64{id}, stopped, "stop hook must fire for the accepted workspace")
}

// TestBatchDelete_MixedBatch checks per-id classification across a single batch:
// one accepted, one not_found, one already_deleting.
func TestBatchDelete_MixedBatch(t *testing.T) {
	ctx := context.Background()
	svc, db := newWorkspaceServiceForTest(t)
	db.SetMaxOpenConns(1)
	q := store.New(db)

	okID := newBatchDeleteWorkspace(t, svc, "ws-ok")
	dupID := newBatchDeleteWorkspace(t, svc, "ws-dup")
	require.NoError(t, q.UpdateWorkspaceStatus(ctx, store.UpdateWorkspaceStatusParams{Status: "deleting", ID: dupID}))

	res := svc.BatchDelete(ctx, []int64{okID, 888888, dupID}, true, nil)

	require.Equal(t, []int64{okID}, res.Accepted)

	reasons := map[int64]string{}
	for _, s := range res.Skipped {
		reasons[s.ID] = s.Reason
	}
	require.Equal(t, "not_found", reasons[888888])
	require.Equal(t, "already_deleting", reasons[dupID])

	// Let the accepted workspace's async cleanup finish before the test DB closes.
	require.Eventually(t, func() bool {
		_, err := q.GetWorkspace(ctx, okID)
		return err != nil
	}, 30*time.Second, 50*time.Millisecond)
}
