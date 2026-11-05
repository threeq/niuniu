package service

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
)

// TestAssertTargetIssueInProject pins the scope of "accept into an existing
// task": the target issue must live in the decision's own project. Without the
// guard, a caller could pass an arbitrary issue id and have the accepted
// message delivered into another project's (or tenant's) workspace.
func TestAssertTargetIssueInProject(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(store.Schema)
	require.NoError(t, err)
	store.Migrate(db)

	q := store.New(db)
	ctx := context.Background()

	projA, err := q.CreateProject(ctx, store.CreateProjectParams{Name: "A", OwnerType: "user", OwnerID: 1})
	require.NoError(t, err)
	colA, err := q.CreateColumn(ctx, store.CreateColumnParams{ProjectID: projA.ID, Name: "todo", Position: 0})
	require.NoError(t, err)
	issueA, err := q.CreateIssue(ctx, store.CreateIssueParams{ColumnID: colA.ID, Title: "a", Position: 0})
	require.NoError(t, err)

	projB, err := q.CreateProject(ctx, store.CreateProjectParams{Name: "B", OwnerType: "user", OwnerID: 1})
	require.NoError(t, err)
	colB, err := q.CreateColumn(ctx, store.CreateColumnParams{ProjectID: projB.ID, Name: "todo", Position: 0})
	require.NoError(t, err)
	issueB, err := q.CreateIssue(ctx, store.CreateIssueParams{ColumnID: colB.ID, Title: "b", Position: 0})
	require.NoError(t, err)

	// 0 = create a new task, always allowed.
	require.NoError(t, assertTargetIssueInProject(ctx, q, projA.ID, 0))
	// Same project: allowed.
	require.NoError(t, assertTargetIssueInProject(ctx, q, projA.ID, issueA.ID))
	// Other project: rejected.
	err = assertTargetIssueInProject(ctx, q, projA.ID, issueB.ID)
	require.ErrorContains(t, err, "another project")
	// Unknown issue: rejected.
	err = assertTargetIssueInProject(ctx, q, projA.ID, 999999)
	require.ErrorContains(t, err, "not found")
}
