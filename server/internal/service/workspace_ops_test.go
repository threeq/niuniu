package service_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// setupMarkDoneDB opens an in-memory SQLite, applies schema/migrations, and
// returns the raw *sql.DB plus a sqlc *Queries. Mirrors setupKanbanTest's
// pattern (no internal/testing import, to avoid the import cycle that
// isolation_suite.go would introduce).
func setupMarkDoneDB(t *testing.T) (*sql.DB, *store.Queries) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	store.Driver = "sqlite"
	require.NoError(t, store.ApplySchema(db))
	store.Migrate(db)
	return db, store.New(db)
}

// setupMarkDoneTest builds a WorkspaceOpsService backed by a real DB, plus a
// project/column/issue/workspace with the workspace pre-seeded at `status`.
// Returns the ops service, the raw *sql.DB (so tests that need to disable
// FKs / hack rows can do so), the *store.Queries handle, the workspace ID,
// and the linked issue ID.
//
// We do NOT mock kanbanSvc — plan §1.1 requires the real service so the
// lifecycle propagation in TestMarkWorkspaceDone_UpdatesIssueLifecycle
// exercises the full path through KanbanService.UpdateIssueLifecycle.
func setupMarkDoneTest(t *testing.T, status string) (*service.WorkspaceOpsService, *sql.DB, *store.Queries, int64, int64) {
	t.Helper()
	ctx := context.Background()
	db, q := setupMarkDoneDB(t)

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

	wsSvc := service.NewWorkspaceService(q, db, nil, t.TempDir(), nil, nil)
	activitySvc := service.NewIssueActivityService(q)
	kbSvc := service.NewKanbanService(db, q, activitySvc, nil, nil)
	ops := service.NewWorkspaceOpsService(q, wsSvc, kbSvc, nil)

	return ops, db, q, ws.ID, issue.ID
}

func TestMarkWorkspaceDone_FromCreated(t *testing.T) {
	ops, _, q, wsID, _ := setupMarkDoneTest(t, "created")
	res, err := ops.MarkWorkspaceDone(context.Background(), wsID)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("expected no warnings on happy path, got %v", res.Warnings)
	}
	ws, _ := q.GetWorkspace(context.Background(), wsID)
	if ws.Status != "completed" {
		t.Errorf("workspace status = %q, want %q", ws.Status, "completed")
	}
}

func TestMarkWorkspaceDone_FromNeedsReview(t *testing.T) {
	ops, _, q, wsID, _ := setupMarkDoneTest(t, "needs_review")
	res, err := ops.MarkWorkspaceDone(context.Background(), wsID)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("expected no warnings on happy path, got %v", res.Warnings)
	}
	ws, _ := q.GetWorkspace(context.Background(), wsID)
	if ws.Status != "completed" {
		t.Errorf("workspace status = %q, want %q", ws.Status, "completed")
	}
}

func TestMarkWorkspaceDone_FromAttention(t *testing.T) {
	ops, _, q, wsID, _ := setupMarkDoneTest(t, "attention")
	res, err := ops.MarkWorkspaceDone(context.Background(), wsID)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("expected no warnings on happy path, got %v", res.Warnings)
	}
	ws, _ := q.GetWorkspace(context.Background(), wsID)
	if ws.Status != "completed" {
		t.Errorf("workspace status = %q, want %q", ws.Status, "completed")
	}
}

func TestMarkWorkspaceDone_RejectsRunning(t *testing.T) {
	ops, _, q, wsID, _ := setupMarkDoneTest(t, "running")
	_, err := ops.MarkWorkspaceDone(context.Background(), wsID)
	if !errors.Is(err, service.ErrWorkspaceRunning) {
		t.Fatalf("expected ErrWorkspaceRunning, got %v", err)
	}
	ws, _ := q.GetWorkspace(context.Background(), wsID)
	if ws.Status != "running" {
		t.Errorf("workspace status should be unchanged, got %q", ws.Status)
	}
}

func TestMarkWorkspaceDone_IdempotentOnCompleted(t *testing.T) {
	ops, _, _, wsID, _ := setupMarkDoneTest(t, "completed")
	if _, err := ops.MarkWorkspaceDone(context.Background(), wsID); err != nil {
		t.Fatalf("expected nil on already-completed, got %v", err)
	}
}

func TestMarkWorkspaceDone_UpdatesIssueLifecycle(t *testing.T) {
	ops, _, q, wsID, issueID := setupMarkDoneTest(t, "needs_review")
	_, _ = ops.MarkWorkspaceDone(context.Background(), wsID)
	issue, err := q.GetIssue(context.Background(), issueID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if issue.LifecycleStatus != "completed" {
		t.Errorf("issue lifecycle_status = %q, want completed", issue.LifecycleStatus)
	}
}

// TestMarkWorkspaceDone_ResetsStaleRunningExecStatus is the regression for the
// stuck "执行中" badge: a standalone issue (no parent, not an epic) whose
// exec_status was driven to an active state (e.g. ask_user answered -> 'running')
// must NOT stay showing the in-flight badge once its workspace completes. The
// no-floor-gate completion path routes through finalizeCompletion, which now
// resolves the active exec_status to the terminal 'done'.
func TestMarkWorkspaceDone_ResetsStaleRunningExecStatus(t *testing.T) {
	ctx := context.Background()
	ops, _, q, wsID, issueID := setupMarkDoneTest(t, "needs_review")

	// Drive the linked issue into an in-flight exec state, as the ask_user
	// answered -> running projection would.
	require.NoError(t, q.SetIssueExecStatus(ctx, store.SetIssueExecStatusParams{
		ExecStatus: "running", ID: issueID,
	}))

	if _, err := ops.MarkWorkspaceDone(ctx, wsID); err != nil {
		t.Fatalf("mark done: %v", err)
	}

	issue, err := q.GetIssue(ctx, issueID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if issue.ExecStatus != "done" {
		t.Errorf("issue exec_status = %q, want done (stuck in-flight badge)", issue.ExecStatus)
	}
}

// TestMarkWorkspaceDone_LeavesIdleExecStatusUntouched guards the other side of
// the fix: an issue that never started execution (exec_status 'idle') must not
// grow a spurious "已完成" exec badge just because its workspace completed. Only
// active states are resolved to 'done'.
func TestMarkWorkspaceDone_LeavesIdleExecStatusUntouched(t *testing.T) {
	ctx := context.Background()
	ops, _, q, wsID, issueID := setupMarkDoneTest(t, "needs_review")

	// Default exec_status on a fresh issue is 'idle'.
	if issue, _ := q.GetIssue(ctx, issueID); issue.ExecStatus != "idle" {
		t.Fatalf("precondition: fresh issue exec_status = %q, want idle", issue.ExecStatus)
	}

	if _, err := ops.MarkWorkspaceDone(ctx, wsID); err != nil {
		t.Fatalf("mark done: %v", err)
	}

	issue, err := q.GetIssue(ctx, issueID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if issue.ExecStatus != "idle" {
		t.Errorf("issue exec_status = %q, want idle (must not fabricate a badge)", issue.ExecStatus)
	}
}

// TestUnmarkWorkspaceDone_RevertsIssueLifecycle verifies the spec §23.7 undo
// card-sync: after un-completing a workspace the linked issue's lifecycle no
// longer reads 'completed', so the card stops claiming Done.
func TestUnmarkWorkspaceDone_RevertsIssueLifecycle(t *testing.T) {
	ctx := context.Background()
	ops, _, q, wsID, issueID := setupMarkDoneTest(t, "needs_review")
	if _, err := ops.MarkWorkspaceDone(ctx, wsID); err != nil {
		t.Fatalf("mark done: %v", err)
	}
	if issue, _ := q.GetIssue(ctx, issueID); issue.LifecycleStatus != "completed" {
		t.Fatalf("precondition: issue should be completed, got %q", issue.LifecycleStatus)
	}
	if err := ops.UnmarkWorkspaceDone(ctx, wsID, "needs_review"); err != nil {
		t.Fatalf("unmark: %v", err)
	}
	ws, _ := q.GetWorkspace(ctx, wsID)
	if ws.Status != "needs_review" {
		t.Errorf("workspace status = %q, want needs_review", ws.Status)
	}
	issue, _ := q.GetIssue(ctx, issueID)
	if issue.LifecycleStatus == "completed" {
		t.Errorf("issue lifecycle still 'completed' after undo; card lies (spec 23.7)")
	}
}

// TestMarkWorkspaceDone_PartialSuccessOnLifecycleFail verifies the
// partial-success contract: when workspace.status flips successfully but
// the linked issue's lifecycle update fails, MarkWorkspaceDone returns
// err == nil with Warnings == ["issue_lifecycle_sync_failed"] and the
// workspace is still authoritatively in 'completed' state.
//
// Construction strategy: schema declares workspaces.issue_id with
// ON DELETE SET NULL, so simply deleting the issue would null out the
// workspace's reference and skip the lifecycle call entirely (no
// partial-success path). We disable foreign_keys for the deletion so
// the workspace keeps a dangling issue_id. KanbanService.UpdateIssueLifecycle
// then GetIssue's the dangling id, hits ErrNoRows, and propagates back to
// MarkWorkspaceDone as a non-fatal lifecycle error.
//
// SetMaxOpenConns(1) in setupMarkDoneDB makes this connection-scoped
// pragma deterministic.
func TestMarkWorkspaceDone_PartialSuccessOnLifecycleFail(t *testing.T) {
	ops, db, q, wsID, issueID := setupMarkDoneTest(t, "needs_review")
	ctx := context.Background()

	// Disable FK enforcement so DELETE on issues doesn't cascade
	// SET NULL on workspaces.issue_id (we want a dangling ref).
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("disable FK: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM issues WHERE id = ?`, issueID); err != nil {
		t.Fatalf("delete issue: %v", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("re-enable FK: %v", err)
	}

	// Sanity-check the dangling ref is still in place (FK reset didn't retroactively
	// scrub orphaned rows — SQLite's PRAGMA only governs new statements).
	ws, err := q.GetWorkspace(ctx, wsID)
	if err != nil {
		t.Fatalf("get workspace: %v", err)
	}
	if !ws.IssueID.Valid || ws.IssueID.Int64 != issueID {
		t.Fatalf("expected dangling issue_id=%d, got valid=%v int64=%d", issueID, ws.IssueID.Valid, ws.IssueID.Int64)
	}

	res, err := ops.MarkWorkspaceDone(ctx, wsID)
	if err != nil {
		t.Fatalf("expected nil error on partial success, got %v", err)
	}
	if len(res.Warnings) != 1 || res.Warnings[0] != "issue_lifecycle_sync_failed" {
		t.Errorf("expected warnings=[issue_lifecycle_sync_failed], got %v", res.Warnings)
	}

	// Workspace must still be authoritatively 'completed' — the lifecycle
	// failure is partial, not a rollback.
	ws, err = q.GetWorkspace(ctx, wsID)
	if err != nil {
		t.Fatalf("get workspace after mark-done: %v", err)
	}
	if ws.Status != "completed" {
		t.Errorf("workspace status = %q, want %q", ws.Status, "completed")
	}
}
