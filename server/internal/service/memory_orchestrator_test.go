package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
)

// fakeMaintBoard simulates the kanban + workspace machinery the orchestrator
// drives, WITHOUT a live agent. onStart stands in for the agent run that happens
// when the standalone workspace launches: it lets a test mutate the real memory
// library (as the agent would via niuniu-mcp) so the end-to-end "memory
// self-corrects on a project change" behaviour is observable. WorkspaceProgress
// reports "running" for runningPolls calls, then idle with cost (so the
// completion poll terminates).
type fakeMaintBoard struct {
	mu sync.Mutex

	cols    []store.Column
	hasRepo bool

	nextIssue int64
	created   map[int64]int64 // issueID -> columnID it was created in

	nextWS    int64
	startedWS map[int64]int64 // issueID -> workspace id
	startErr  error
	onStart   func(issueID int64) // simulate the agent's memory edits

	runningPolls int // WorkspaceProgress returns running until this many polls elapse
	polls        int
	cost         float64
	turns        int64
}

func newFakeBoard() *fakeMaintBoard {
	return &fakeMaintBoard{
		cols:         []store.Column{{ID: 10, Position: 0, Name: "待办"}, {ID: 20, Position: 1, Name: "完成", LifecycleMapping: "completed"}},
		hasRepo:      true,
		created:      map[int64]int64{},
		startedWS:    map[int64]int64{},
		runningPolls: 1,
		cost:         0.42,
		turns:        7,
	}
}

func (b *fakeMaintBoard) Columns(context.Context, int64) ([]store.Column, error) { return b.cols, nil }

func (b *fakeMaintBoard) HasRepository(context.Context, int64) (bool, error) { return b.hasRepo, nil }

func (b *fakeMaintBoard) CreateIssue(_ context.Context, columnID int64, _, _ string) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextIssue++
	b.created[b.nextIssue] = columnID
	return b.nextIssue, nil
}

func (b *fakeMaintBoard) StartIssueStandalone(_ context.Context, issueID, _ int64, _ string) (int64, error) {
	b.mu.Lock()
	if b.startErr != nil {
		err := b.startErr
		b.mu.Unlock()
		return 0, err
	}
	b.nextWS++
	wsID := 900 + b.nextWS
	b.startedWS[issueID] = wsID
	run := b.onStart
	b.mu.Unlock()
	if run != nil {
		run(issueID) // the "agent" edits memory the moment the workspace runs
	}
	return wsID, nil
}

func (b *fakeMaintBoard) WorkspaceProgress(context.Context, int64) (bool, float64, int64, int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.polls++
	running := b.polls <= b.runningPolls
	interactions := int64(0)
	if b.polls > b.runningPolls {
		interactions = 3 // the agent has run
	}
	return running, b.cost, b.turns, interactions, nil
}

var fastCfg = MemoryMaintConfig{Poll: time.Millisecond, Timeout: 2 * time.Second}

// beginMaintenance with no repository surfaces ErrNoRepository and creates nothing.
func TestBeginMaintenance_NoRepository(t *testing.T) {
	svc, q, _, ctx := newEvolveSvc(t)
	pid := newEvolveProject(t, q, ctx)
	board := newFakeBoard()
	board.hasRepo = false

	_, err := svc.StartMemoryMaintenanceOnce(ctx, board, pid, 1)
	require.ErrorIs(t, err, ErrNoRepository)
	require.Equal(t, int64(0), board.nextIssue, "no issue created for a repo-less project")

	runs, err := svc.ListSweepRuns(ctx, pid, 10)
	require.NoError(t, err)
	require.Empty(t, runs, "no run recorded")
}

// The manual trigger creates the maintenance issue synchronously and records a
// run (the background workspace start is covered by driveMaintenance below).
func TestStartMemoryMaintenanceOnce_CreatesIssueAndRecords(t *testing.T) {
	svc, q, _, ctx := newEvolveSvc(t)
	pid := newEvolveProject(t, q, ctx)
	board := newFakeBoard()

	res, err := svc.StartMemoryMaintenanceOnce(ctx, board, pid, 1)
	require.NoError(t, err)
	require.Greater(t, res.IssueID, int64(0), "issue created and returned")
	require.Equal(t, int64(10), board.created[res.IssueID], "created in the first column")

	runs, err := svc.ListSweepRuns(ctx, pid, 10)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Equal(t, "manual", runs[0].Trigger)
}

// driveMaintenance: a project changes, the standalone agent corrects the memory
// library (here: archives a stale entry), and the run log is backfilled with the
// workspace id + token/cost consumption once the workspace goes idle. The
// correction is reversible.
func TestDriveMaintenance_SelfCorrectsAndBackfillsConsumption(t *testing.T) {
	svc, q, _, ctx := newEvolveSvc(t)
	pid := newEvolveProject(t, q, ctx)

	stale, err := svc.Create(ctx, CreateMemoryInput{
		Owner: OwnerRef{Type: "user", ID: 0}, ProjectID: &pid,
		MemType: "gotcha", Title: "outdated insight", Content: "old behavior", Source: "extract",
	})
	require.NoError(t, err)

	board := newFakeBoard()
	board.onStart = func(int64) { require.NoError(t, svc.SoftDelete(ctx, stale.ID)) }

	// Create the issue + initial run row, then drive synchronously.
	issueID, err := board.CreateIssue(ctx, 10, MemoryMaintIssueTitle, memoryMaintPrompt)
	require.NoError(t, err)
	runID := svc.recordMaintRun(ctx, pid, "manual", MemoryMaintResult{IssueID: issueID})
	svc.driveMaintenance(ctx, board, pid, issueID, runID, 1, fastCfg)

	// Memory self-corrected and the change is reversible.
	got, err := svc.Get(ctx, stale.ID)
	require.NoError(t, err)
	require.True(t, got.DeletedAt.Valid, "stale memory archived by the maintenance agent")
	require.NoError(t, svc.Restore(ctx, stale.ID))
	got, err = svc.Get(ctx, stale.ID)
	require.NoError(t, err)
	require.False(t, got.DeletedAt.Valid, "archival is reversible")

	// The run log is backfilled with workspace id + consumption (B2).
	runs, err := svc.ListSweepRuns(ctx, pid, 10)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Contains(t, runs[0].Detail, "\"completed\":true")
	require.Contains(t, runs[0].Detail, "cost_usd")
	require.Contains(t, runs[0].Detail, "workspace_id")
}

// On a start failure the run is backfilled with the error and nothing is deleted.
func TestDriveMaintenance_StartFailureRecorded(t *testing.T) {
	svc, q, _, ctx := newEvolveSvc(t)
	pid := newEvolveProject(t, q, ctx)
	board := newFakeBoard()
	board.startErr = context.DeadlineExceeded

	issueID, err := board.CreateIssue(ctx, 10, MemoryMaintIssueTitle, memoryMaintPrompt)
	require.NoError(t, err)
	runID := svc.recordMaintRun(ctx, pid, "manual", MemoryMaintResult{IssueID: issueID})
	svc.driveMaintenance(ctx, board, pid, issueID, runID, 1, fastCfg)

	runs, err := svc.ListSweepRuns(ctx, pid, 10)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Contains(t, runs[0].Detail, "error")
}

// The scheduler skips disabled projects (empty schedule) and repo-less projects.
func TestRunDueMaintenance_SkipsDisabledAndRepoless(t *testing.T) {
	svc, q, _, ctx := newEvolveSvc(t)
	pid := newEvolveProject(t, q, ctx)
	board := newFakeBoard()
	board.hasRepo = false // even if enabled, repo-less is skipped

	// Disabled by default (empty schedule): nothing runs.
	svc.runDueMaintenance(ctx, board)
	runs, err := svc.ListSweepRuns(ctx, pid, 10)
	require.NoError(t, err)
	require.Empty(t, runs, "disabled project is skipped")

	// Enabled but repo-less: maybeRun bails before creating anything.
	require.NoError(t, svc.SetProjectSweepCron(ctx, pid, DefaultSweepCron))
	svc.runDueMaintenance(ctx, board)
	runs, err = svc.ListSweepRuns(ctx, pid, 10)
	require.NoError(t, err)
	require.Empty(t, runs, "enabled but repo-less project is skipped")
	require.Equal(t, int64(0), board.nextIssue, "no maintenance issue created")
}
