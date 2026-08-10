package service_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// MoveIssueRunAware fixture helpers + test doubles
// ---------------------------------------------------------------------------

// recordedCall captures method name + args for fake doubles.
type recordedCall struct {
	Name string
	Args []any
}

// setupRunSvcWithDoubles creates an in-memory DB + RunService with a live event
// bus wired. A card move never interrupts the in-flight agent (decision
// 2026-06-08), so there is no workspace-interruptor double.
func setupRunSvcWithDoubles(t *testing.T) (svc *service.RunService, db *sql.DB, ctx context.Context,
	bus *event.Bus) {
	t.Helper()
	var err error
	db, err = sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	_, err = db.Exec(store.Schema)
	require.NoError(t, err)
	store.Migrate(db)
	q := store.New(db)
	bus = event.NewBus()
	svc = service.NewRunService(db, q, bus)
	ctx = context.Background()
	return
}

// ---------------------------------------------------------------------------
// MoveIssueRunAware branch tests (bare board move main road)
// ---------------------------------------------------------------------------

// Bare move: no agent interrupt when there is no in-flight workspace.
func TestRunService_MoveIssueRunAware_BareNoRun(t *testing.T) {
	svc, db, ctx, _ := setupRunSvcWithDoubles(t)
	uid := kanbanCreateUser(t, db, "u-bnr")
	pid := kanbanCreateProject(t, db, "user", uid, "p-bnr")
	src := kanbanCreateColumn(t, db, pid, "todo")
	dst := kanbanCreateColumn(t, db, pid, "doing")
	iid := kanbanCreateIssue(t, db, src, "task")

	out, err := svc.MoveIssueRunAware(ctx, service.MoveIssueInput{
		IssueID: iid, TargetColumnID: dst, TargetPosition: 0, Source: service.MoveSourceDrag,
	})
	require.NoError(t, err)
	assert.Equal(t, service.BranchBareNoRun, out.Branch)
	assert.Equal(t, int64(0), out.RunID)
	assert.Empty(t, out.NewStatus)

	q := store.New(db)
	issue, _ := q.GetIssue(ctx, iid)
	assert.Equal(t, dst, issue.ColumnID)
}

// fakeInstructDispatcher is a test double for ColumnInstructDispatcher.
// It records calls via a channel so the goroutine dispatch can be awaited.
type fakeInstructDispatcher struct {
	calls chan recordedCall
}

func newFakeInstructDispatcher() *fakeInstructDispatcher {
	return &fakeInstructDispatcher{calls: make(chan recordedCall, 4)}
}

func (f *fakeInstructDispatcher) DispatchInstructColumn(_ context.Context, issueID, columnID, callerUserID int64) {
	f.calls <- recordedCall{"dispatch", []any{issueID, columnID, callerUserID}}
}

// awaitDispatch waits up to 1 s for a DispatchInstructColumn call. Returns the
// recorded call or fails the test if the call doesn't arrive in time.
func (f *fakeInstructDispatcher) awaitDispatch(t *testing.T) recordedCall {
	t.Helper()
	select {
	case c := <-f.calls:
		return c
	case <-time.After(1 * time.Second):
		t.Fatal("DispatchInstructColumn was not called within 1s")
		return recordedCall{}
	}
}

// Human drag to an instruct-primitive column triggers DispatchInstructColumn.
func TestRunService_MoveIssueRunAware_BareNoRun_DragToInstruct_DispatchesCalled(t *testing.T) {
	svc, db, ctx, _ := setupRunSvcWithDoubles(t)
	disp := newFakeInstructDispatcher()
	svc.WithInstructDispatcher(disp)

	uid := kanbanCreateUser(t, db, "u-di1")
	pid := kanbanCreateProject(t, db, "user", uid, "p-di1")
	src := kanbanCreateColumn(t, db, pid, "todo")
	dst := kanbanCreateColumn(t, db, pid, "implement")
	// Mark dst as instruct-primitive via raw SQL (op_primitive added by stage-1a migration).
	_, err := db.ExecContext(ctx, `UPDATE columns SET op_primitive = 'instruct' WHERE id = ?`, dst)
	require.NoError(t, err)
	iid := kanbanCreateIssue(t, db, src, "fix bug")

	out, err := svc.MoveIssueRunAware(ctx, service.MoveIssueInput{
		IssueID: iid, TargetColumnID: dst, TargetPosition: 0,
		Source: service.MoveSourceDrag, ActorUserID: uid,
	})
	require.NoError(t, err)
	assert.Equal(t, service.BranchBareNoRun, out.Branch)

	call := disp.awaitDispatch(t)
	assert.Equal(t, iid, call.Args[0], "issueID")
	assert.Equal(t, dst, call.Args[1], "columnID")
	assert.Equal(t, uid, call.Args[2], "callerUserID")
}

// MCP advance_issue must NOT trigger the instruct dispatcher — AdvanceIssue
// already handles workspace creation + kickoff on that path; dispatching here
// would double-kickoff the agent.
func TestRunService_MoveIssueRunAware_BareNoRun_MCPAdvance_NoDispatch(t *testing.T) {
	svc, db, ctx, _ := setupRunSvcWithDoubles(t)
	disp := newFakeInstructDispatcher()
	svc.WithInstructDispatcher(disp)

	uid := kanbanCreateUser(t, db, "u-di2")
	pid := kanbanCreateProject(t, db, "user", uid, "p-di2")
	src := kanbanCreateColumn(t, db, pid, "todo")
	dst := kanbanCreateColumn(t, db, pid, "implement")
	_, err := db.ExecContext(ctx, `UPDATE columns SET op_primitive = 'instruct' WHERE id = ?`, dst)
	require.NoError(t, err)
	iid := kanbanCreateIssue(t, db, src, "fix bug")

	_, err = svc.MoveIssueRunAware(ctx, service.MoveIssueInput{
		IssueID: iid, TargetColumnID: dst, TargetPosition: 0,
		Source: service.MoveSourceMCPAdvance, ActorUserID: uid,
	})
	require.NoError(t, err)

	// Give the goroutine a moment to fire (it shouldn't).
	time.Sleep(50 * time.Millisecond)
	select {
	case <-disp.calls:
		t.Fatal("DispatchInstructColumn must not be called for MCP-advance moves")
	default:
	}
}

// Cross-project column move is rejected.
func TestRunService_MoveIssueRunAware_RejectsCrossProjectColumn(t *testing.T) {
	svc, db, ctx, _ := setupRunSvcWithDoubles(t)
	uid := kanbanCreateUser(t, db, "u-cross")
	pid := kanbanCreateProject(t, db, "user", uid, "p-cross-a")
	src := kanbanCreateColumn(t, db, pid, "todo")
	iid := kanbanCreateIssue(t, db, src, "task")

	otherPid := kanbanCreateProject(t, db, "user", uid, "p-cross-b")
	otherCol := kanbanCreateColumn(t, db, otherPid, "elsewhere")

	_, err := svc.MoveIssueRunAware(ctx, service.MoveIssueInput{
		IssueID: iid, TargetColumnID: otherCol, TargetPosition: 0, Source: service.MoveSourceDrag,
	})
	require.ErrorIs(t, err, service.ErrTargetColumnNotInProject)
}

// Bare drag-to-Done reverse-syncs lifecycle_status from the target column mapping.
func TestRunService_MoveIssueRunAware_BareNoRun_KanbanEventPublished(t *testing.T) {
	svc, db, ctx, bus := setupRunSvcWithDoubles(t)
	uid := kanbanCreateUser(t, db, "u-kev")
	pid := kanbanCreateProject(t, db, "user", uid, "p-kev")
	src := kanbanCreateColumn(t, db, pid, "todo")
	dst := kanbanCreateColumn(t, db, pid, "doing")
	iid := kanbanCreateIssue(t, db, src, "task")

	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)

	_, err := svc.MoveIssueRunAware(ctx, service.MoveIssueInput{
		IssueID: iid, TargetColumnID: dst, TargetPosition: 0, Source: service.MoveSourceDrag,
	})
	require.NoError(t, err)

	select {
	case e := <-ch:
		assert.Equal(t, event.EventKanbanUpdate, e.Type)
	case <-time.After(2 * time.Second):
		t.Fatal("expected kanban_update event")
	}
}

// A cross-column move records a "moved" activity on the issue timeline so the
// move shows up in the issue detail panel (covers both human drag and the AI
// advance_issue / abandon_issue paths, which all converge on MoveIssueRunAware).
func TestRunService_MoveIssueRunAware_RecordsMovedActivity(t *testing.T) {
	svc, db, ctx, _ := setupRunSvcWithDoubles(t)
	q := store.New(db)
	svc.WithActivityService(service.NewIssueActivityService(q))

	uid := kanbanCreateUser(t, db, "u-mov")
	pid := kanbanCreateProject(t, db, "user", uid, "p-mov")
	src := kanbanCreateColumn(t, db, pid, "todo")
	dst := kanbanCreateColumn(t, db, pid, "doing")
	iid := kanbanCreateIssue(t, db, src, "task")

	_, err := svc.MoveIssueRunAware(ctx, service.MoveIssueInput{
		IssueID: iid, TargetColumnID: dst, TargetPosition: 0, Source: service.MoveSourceMCPAdvance,
	})
	require.NoError(t, err)

	acts, err := q.ListActivitiesByIssue(ctx, iid)
	require.NoError(t, err)
	var moved *store.IssueActivity
	for i := range acts {
		if acts[i].Action == "moved" {
			moved = &acts[i]
			break
		}
	}
	require.NotNil(t, moved, "expected a 'moved' activity on the issue timeline")
	assert.Equal(t, "todo", moved.OldValue.String)
	assert.Equal(t, "doing", moved.NewValue.String)
}

// A same-column reorder is not a column move and must not record a "moved" activity.
func TestRunService_MoveIssueRunAware_SameColumnNoMovedActivity(t *testing.T) {
	svc, db, ctx, _ := setupRunSvcWithDoubles(t)
	q := store.New(db)
	svc.WithActivityService(service.NewIssueActivityService(q))

	uid := kanbanCreateUser(t, db, "u-same")
	pid := kanbanCreateProject(t, db, "user", uid, "p-same")
	col := kanbanCreateColumn(t, db, pid, "todo")
	iid := kanbanCreateIssue(t, db, col, "task")

	_, err := svc.MoveIssueRunAware(ctx, service.MoveIssueInput{
		IssueID: iid, TargetColumnID: col, TargetPosition: 1, Source: service.MoveSourceDrag,
	})
	require.NoError(t, err)

	acts, err := q.ListActivitiesByIssue(ctx, iid)
	require.NoError(t, err)
	for _, a := range acts {
		assert.NotEqual(t, "moved", a.Action, "same-column reorder must not log a move")
	}
}
