package service_test

// Epic 统一分支 Wave 2 (spec docs/superpowers/specs/2026-09-28-epic-unified-branch-design.md
// §3 普通任务工作空间的父子护栏)。
//
// Guard under test: an issue (or a plain-task parent) that already has a live
// (non-archived) workspace must not gain/lose parent/child relations, because
// the workspace's branch baseline can no longer follow an epic re-parenting.
// Epic-typed parents keep accepting children — their workspace IS the
// orchestration control workspace, and dispatching children to it is the epic
// workflow itself.

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// guardSeed bundles a project/column plus two standalone issues.
type guardSeed struct {
	svc     *service.KanbanService
	db      *sql.DB
	ctx     context.Context
	colID   int64
	issueA  int64
	issueB  int64
	epicID  int64
}

func setupHierarchyGuardTest(t *testing.T) *guardSeed {
	t.Helper()
	svc, db, ctx := setupKanbanTest(t)
	project := createTestProject(t, db, ctx)
	col, err := svc.CreateColumn(ctx, project.ID, "待办", "")
	require.NoError(t, err)

	mk := func(title string, parent *int64, issueType string) int64 {
		d, err := svc.CreateIssue(ctx, col.ID, title, "", 0, 0, "", "", "", 0, parent, issueType, 0, nil, nil, 0)
		require.NoError(t, err)
		return d.ID
	}
	a := mk("普通任务A", nil, "task")
	b := mk("普通任务B", nil, "task")
	epic := mk("epic", nil, "epic")
	return &guardSeed{svc: svc, db: db, ctx: ctx, colID: col.ID, issueA: a, issueB: b, epicID: epic}
}

// attachActiveWorkspace inserts a live workspace row bound to the issue.
func (g *guardSeed) attachActiveWorkspace(t *testing.T, issueID int64) {
	t.Helper()
	g.attachWorkspace(t, issueID, 0)
}

// attachArchivedWorkspace inserts an archived workspace row bound to the issue.
func (g *guardSeed) attachArchivedWorkspace(t *testing.T, issueID int64) {
	t.Helper()
	g.attachWorkspace(t, issueID, 1)
}

func (g *guardSeed) attachWorkspace(t *testing.T, issueID, archived int64) {
	t.Helper()
	_, err := g.db.Exec(
		`INSERT INTO workspaces (issue_id, name, path, status, owner_type, owner_id, is_archived)
		 VALUES (?, ?, ?, 'created', 'user', 1, ?)`,
		issueID, "guard-ws", t.TempDir(), archived)
	require.NoError(t, err)
}

// hasParent reports the issue's stored parent (0 = none).
func (g *guardSeed) parentOf(t *testing.T, issueID int64) int64 {
	t.Helper()
	q := store.New(g.db)
	iss, err := q.GetIssue(g.ctx, issueID)
	require.NoError(t, err)
	if !iss.ParentIssueID.Valid {
		return 0
	}
	return iss.ParentIssueID.Int64
}

// TestHierarchyGuard_IssueWithWorkspace_CannotBecomeChild pins guard (a): the
// issue itself has an active workspace, so setting a parent on it is refused
// and the relation stays unchanged.
func TestHierarchyGuard_IssueWithWorkspace_CannotBecomeChild(t *testing.T) {
	g := setupHierarchyGuardTest(t)
	g.attachActiveWorkspace(t, g.issueA)

	_, err := g.svc.SetIssueExecFields(g.ctx, g.issueA, &g.issueB, "task", 0, "idle")
	require.Error(t, err)
	assert.True(t, errors.Is(err, service.ErrIssueHasWorkspace), "want ErrIssueHasWorkspace, got %v", err)
	assert.Equal(t, int64(0), g.parentOf(t, g.issueA), "relation must stay unchanged")
}

// TestHierarchyGuard_PlainParentWithWorkspace_CannotTakeChild pins guard (b):
// a plain (task-typed) parent with an active workspace cannot collect a child,
// via SetIssueExecFields…
func TestHierarchyGuard_PlainParentWithWorkspace_CannotTakeChild(t *testing.T) {
	g := setupHierarchyGuardTest(t)
	g.attachActiveWorkspace(t, g.issueA)

	_, err := g.svc.SetIssueExecFields(g.ctx, g.issueB, &g.issueA, "task", 0, "idle")
	require.Error(t, err)
	assert.True(t, errors.Is(err, service.ErrParentHasWorkspace), "want ErrParentHasWorkspace, got %v", err)
	assert.Equal(t, int64(0), g.parentOf(t, g.issueB), "relation must stay unchanged")
}

// TestHierarchyGuard_CreateIssueUnderParentWithWorkspace_Rejected pins the
// creation-path guard: CreateIssue with parent_issue_id is rejected when the
// parent already has an active workspace…
func TestHierarchyGuard_CreateIssueUnderParentWithWorkspace_Rejected(t *testing.T) {
	g := setupHierarchyGuardTest(t)
	g.attachActiveWorkspace(t, g.issueA)

	_, err := g.svc.CreateIssue(g.ctx, g.colID, "新子任务", "", 0, 0, "", "", "", 0, &g.issueA, "task", 0, nil, nil, 0)
	require.Error(t, err)
	assert.True(t, errors.Is(err, service.ErrParentHasWorkspace), "want ErrParentHasWorkspace, got %v", err)
}

// TestHierarchyGuard_BatchCreateUnderParentWithWorkspace_Rejected pins the same
// guard on batch_create_issues (the orchestration agent's create path must hit
// the same rule for plain-task parents).
func TestHierarchyGuard_BatchCreateUnderParentWithWorkspace_Rejected(t *testing.T) {
	g := setupHierarchyGuardTest(t)
	g.attachActiveWorkspace(t, g.issueA)

	// Resolve the project id from the column.
	col, err := g.svc.GetColumn(g.ctx, g.colID)
	require.NoError(t, err)
	parent := g.issueA
	_, err = g.svc.BatchCreateIssues(g.ctx, col.ProjectID, []service.BatchCreateIssuesTask{
		{Title: "批量子任务", ParentIssueID: &parent},
	}, 0)
	require.Error(t, err)
	assert.True(t, errors.Is(err, service.ErrParentHasWorkspace), "want ErrParentHasWorkspace, got %v", err)
}

// TestHierarchyGuard_WithoutWorkspace_ParentFlowSucceeds pins the happy path:
// with no workspace anywhere the full parent/child flows keep working.
func TestHierarchyGuard_WithoutWorkspace_ParentFlowSucceeds(t *testing.T) {
	g := setupHierarchyGuardTest(t)

	// CreateIssue with a parent.
	d, err := g.svc.CreateIssue(g.ctx, g.colID, "正常子任务", "", 0, 0, "", "", "", 0, &g.issueA, "task", 0, nil, nil, 0)
	require.NoError(t, err)
	assert.Equal(t, g.issueA, g.parentOf(t, d.ID))

	// SetIssueExecFields attaches B under A and re-parents it to the epic.
	_, err = g.svc.SetIssueExecFields(g.ctx, g.issueB, &g.issueA, "task", 0, "idle")
	require.NoError(t, err)
	assert.Equal(t, g.issueA, g.parentOf(t, g.issueB))
	_, err = g.svc.SetIssueExecFields(g.ctx, g.issueB, &g.epicID, "task", 1, "idle")
	require.NoError(t, err)
	assert.Equal(t, g.epicID, g.parentOf(t, g.issueB))
}

// TestHierarchyGuard_ArchivedWorkspace_DoesNotBlock pins the 判定's
// is_archived=0 clause: only LIVE workspaces trigger the guard.
func TestHierarchyGuard_ArchivedWorkspace_DoesNotBlock(t *testing.T) {
	g := setupHierarchyGuardTest(t)
	g.attachArchivedWorkspace(t, g.issueA)
	g.attachArchivedWorkspace(t, g.issueB)

	_, err := g.svc.SetIssueExecFields(g.ctx, g.issueA, &g.issueB, "task", 0, "idle")
	require.NoError(t, err, "archived workspace must not block re-parenting")
	assert.Equal(t, g.issueB, g.parentOf(t, g.issueA))
}

// TestHierarchyGuard_EpicParentWithWorkspace_AcceptsChildren pins the
// orchestration exemption: an epic-typed parent's active workspace is its
// orchestration control workspace — creating children under it (the epic
// workflow, both single and batch) must keep working.
func TestHierarchyGuard_EpicParentWithWorkspace_AcceptsChildren(t *testing.T) {
	g := setupHierarchyGuardTest(t)
	g.attachActiveWorkspace(t, g.epicID)

	d, err := g.svc.CreateIssue(g.ctx, g.colID, "epic 新子任务", "", 0, 0, "", "", "", 0, &g.epicID, "task", 0, nil, nil, 0)
	require.NoError(t, err)
	assert.Equal(t, g.epicID, g.parentOf(t, d.ID))

	// The batch path (what the orchestration agent uses) also stays open.
	col, err := g.svc.GetColumn(g.ctx, g.colID)
	require.NoError(t, err)
	res, err := g.svc.BatchCreateIssues(g.ctx, col.ProjectID, []service.BatchCreateIssuesTask{
		{Title: "epic 批量子任务", ParentIssueID: &g.epicID},
	}, 0)
	require.NoError(t, err)
	require.Len(t, res.Issues, 1)
	assert.Equal(t, g.epicID, g.parentOf(t, res.Issues[0].ID))

	// Attaching an existing childless issue under the epic via SetIssueExecFields.
	_, err = g.svc.SetIssueExecFields(g.ctx, g.issueB, &g.epicID, "task", 0, "idle")
	require.NoError(t, err)
	assert.Equal(t, g.epicID, g.parentOf(t, g.issueB))
}

// TestHierarchyGuard_SameParentRewriteAllowed pins the "设置/变更" reading: a
// request that re-states the issue's CURRENT parent changes nothing, so it is
// not a relation change and must not trip the guard (MCP update_issue often
// echoes the unchanged parent back).
func TestHierarchyGuard_SameParentRewriteAllowed(t *testing.T) {
	g := setupHierarchyGuardTest(t)
	// Attach B under the epic FIRST, then give both sides workspaces.
	_, err := g.svc.SetIssueExecFields(g.ctx, g.issueB, &g.epicID, "task", 0, "idle")
	require.NoError(t, err)
	g.attachActiveWorkspace(t, g.issueB)
	g.attachActiveWorkspace(t, g.epicID)

	_, err = g.svc.SetIssueExecFields(g.ctx, g.issueB, &g.epicID, "task", 0, "idle")
	require.NoError(t, err, "re-stating the current parent is not a relation change")
	assert.Equal(t, g.epicID, g.parentOf(t, g.issueB))
}
