package service_test

import (
	"testing"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBatchMoveIssues_MovesAllToTargetEnd checks two issues from column A move
// to the end of column B preserving relative order.
func TestBatchMoveIssues_MovesAllToTargetEnd(t *testing.T) {
	svc, db, ctx := setupKanbanTest(t)
	user := kanbanCreateUser(t, db, "alice")
	pid := kanbanCreateProject(t, db, "user", user, "p1")
	colA := kanbanCreateColumn(t, db, pid, "A")
	colB := kanbanCreateColumn(t, db, pid, "B")
	// Seed B with one preexisting issue so we can assert "appended to end".
	existing := kanbanCreateIssue(t, db, colB, "existing")
	i1 := kanbanCreateIssue(t, db, colA, "first")
	i2 := kanbanCreateIssue(t, db, colA, "second")

	res, err := svc.BatchMoveIssues(ctx, []int64{i1, i2}, colB, user)
	require.NoError(t, err)
	assert.ElementsMatch(t, []int64{i1, i2}, res.Succeeded)
	assert.Empty(t, res.Skipped)

	got, err := svc.ListIssues(ctx, colB)
	require.NoError(t, err)
	require.Len(t, got, 3)
	// Order: existing (pos 0), i1 (pos 1), i2 (pos 2).
	assert.Equal(t, existing, got[0].ID)
	assert.Equal(t, i1, got[1].ID)
	assert.Equal(t, i2, got[2].ID)
}

// TestBatchMoveIssues_CrossProjectSkipped verifies that an issue belonging to
// a different project is skipped with reason "cross_project".
func TestBatchMoveIssues_CrossProjectSkipped(t *testing.T) {
	svc, db, ctx := setupKanbanTest(t)
	user := kanbanCreateUser(t, db, "alice")
	pidA := kanbanCreateProject(t, db, "user", user, "pa")
	pidB := kanbanCreateProject(t, db, "user", user, "pb")
	colA := kanbanCreateColumn(t, db, pidA, "A")
	colB := kanbanCreateColumn(t, db, pidB, "B")
	i1 := kanbanCreateIssue(t, db, colA, "in-a")

	res, err := svc.BatchMoveIssues(ctx, []int64{i1}, colB, user)
	require.NoError(t, err)
	assert.Empty(t, res.Succeeded)
	require.Len(t, res.Skipped, 1)
	assert.Equal(t, i1, res.Skipped[0].ID)
	assert.Equal(t, "cross_project", res.Skipped[0].Reason)
}

// TestBatchMoveIssues_NotFoundSkipped verifies that a non-existent id is
// skipped with reason "not_found" (rest of batch still succeeds).
func TestBatchMoveIssues_NotFoundSkipped(t *testing.T) {
	svc, db, ctx := setupKanbanTest(t)
	user := kanbanCreateUser(t, db, "alice")
	pid := kanbanCreateProject(t, db, "user", user, "p1")
	colA := kanbanCreateColumn(t, db, pid, "A")
	colB := kanbanCreateColumn(t, db, pid, "B")
	i1 := kanbanCreateIssue(t, db, colA, "real")

	res, err := svc.BatchMoveIssues(ctx, []int64{i1, 99999}, colB, user)
	require.NoError(t, err)
	assert.ElementsMatch(t, []int64{i1}, res.Succeeded)
	require.Len(t, res.Skipped, 1)
	assert.Equal(t, int64(99999), res.Skipped[0].ID)
	assert.Equal(t, "not_found", res.Skipped[0].Reason)
}

// TestBatchUpdatePriority_SetsAll verifies the priority column updates for
// every provided issue.
func TestBatchUpdatePriority_SetsAll(t *testing.T) {
	svc, db, ctx := setupKanbanTest(t)
	user := kanbanCreateUser(t, db, "alice")
	pid := kanbanCreateProject(t, db, "user", user, "p1")
	col := kanbanCreateColumn(t, db, pid, "A")
	i1 := kanbanCreateIssue(t, db, col, "one")
	i2 := kanbanCreateIssue(t, db, col, "two")

	res, err := svc.BatchUpdatePriority(ctx, []int64{i1, i2}, 3)
	require.NoError(t, err)
	assert.ElementsMatch(t, []int64{i1, i2}, res.Succeeded)
	assert.Empty(t, res.Skipped)

	d1, err := svc.GetIssue(ctx, i1)
	require.NoError(t, err)
	assert.Equal(t, int64(3), d1.Priority.Int64)
	d2, err := svc.GetIssue(ctx, i2)
	require.NoError(t, err)
	assert.Equal(t, int64(3), d2.Priority.Int64)
}

// TestBatchUpdatePriority_OutOfRange rejects an invalid priority before
// touching the DB.
func TestBatchUpdatePriority_OutOfRange(t *testing.T) {
	svc, _, ctx := setupKanbanTest(t)
	_, err := svc.BatchUpdatePriority(ctx, []int64{1}, 7)
	assert.Error(t, err)
}

// TestBatchDeleteIssues_RemovesAll verifies hard-delete of every provided id.
func TestBatchDeleteIssues_RemovesAll(t *testing.T) {
	svc, db, ctx := setupKanbanTest(t)
	user := kanbanCreateUser(t, db, "alice")
	pid := kanbanCreateProject(t, db, "user", user, "p1")
	col := kanbanCreateColumn(t, db, pid, "A")
	i1 := kanbanCreateIssue(t, db, col, "one")
	i2 := kanbanCreateIssue(t, db, col, "two")

	res, err := svc.BatchDeleteIssues(ctx, []int64{i1, i2})
	require.NoError(t, err)
	assert.ElementsMatch(t, []int64{i1, i2}, res.Succeeded)
	assert.Empty(t, res.Skipped)

	_, err = svc.GetIssue(ctx, i1)
	assert.Error(t, err)
	_, err = svc.GetIssue(ctx, i2)
	assert.Error(t, err)
}

// TestBatchResultTypes is a smoke compile-time check that SkippedItem &
// BatchResult are exported and shaped as the handler expects.
func TestBatchResultTypes(t *testing.T) {
	var r service.BatchResult
	r.Succeeded = []int64{1, 2}
	r.Skipped = []service.SkippedItem{{ID: 3, Reason: "forbidden"}}
	assert.Equal(t, "forbidden", r.Skipped[0].Reason)
}

// TestApplyIssueLabelDeltas_AddAndRemoveAtomic verifies that a single call
// applying both an add and a remove across multiple issues commits both
// directions: label B is attached and label A detached on every issue. This is
// the path the batch-labels endpoint takes; doing it in one transaction means a
// partial failure can't leave the adds committed while reporting a total error.
func TestApplyIssueLabelDeltas_AddAndRemoveAtomic(t *testing.T) {
	ks, db, ctx := setupKanbanTest(t)
	ls := service.NewLabelService(db, nil)
	owner := kanbanCreateUser(t, db, "owner-ald")
	pid := kanbanCreateProject(t, db, "user", owner, "p-ald")
	col := kanbanCreateColumn(t, db, pid, "todo")
	i1 := kanbanCreateIssue(t, db, col, "one")
	i2 := kanbanCreateIssue(t, db, col, "two")
	lblA := kanbanCreateLabel(t, db, pid, owner, "a", "#111111")
	lblB := kanbanCreateLabel(t, db, pid, owner, "b", "#222222")

	// Seed both issues with label A.
	require.NoError(t, ls.ApplyIssueLabelDeltas(ctx, []int64{i1, i2}, []int64{lblA}, nil))

	// Single call: add B, remove A across both issues.
	require.NoError(t, ls.ApplyIssueLabelDeltas(ctx, []int64{i1, i2}, []int64{lblB}, []int64{lblA}))

	for _, id := range []int64{i1, i2} {
		d, err := ks.GetIssue(ctx, id)
		require.NoError(t, err)
		ids := make([]int64, 0, len(d.Labels))
		for _, l := range d.Labels {
			ids = append(ids, l.ID)
		}
		assert.ElementsMatch(t, []int64{lblB}, ids, "issue %d should carry only B", id)
	}

	// No-op guard: empty deltas return nil without error.
	require.NoError(t, ls.ApplyIssueLabelDeltas(ctx, []int64{i1}, nil, nil))
}
