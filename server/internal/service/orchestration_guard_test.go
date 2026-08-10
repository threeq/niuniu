package service_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStarter records StartQueuedWorkspace calls so the drain test can assert
// which queued issues were launched. An optional err makes the start fail.
type fakeStarter struct {
	mu      sync.Mutex
	started []int64
	err     error
}

func (f *fakeStarter) StartQueuedWorkspace(_ context.Context, issueID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.started = append(f.started, issueID)
	return nil
}

// addWorkspace inserts a workspace row (owner user/1) linked to issueID with the
// given status, so the guard's active-workspace count and chain-cost sum see it.
func (e *epicTestEnv) addWorkspace(t *testing.T, issueID int64, status string) int64 {
	t.Helper()
	ws, err := e.q.CreateWorkspace(e.ctx, store.CreateWorkspaceParams{
		IssueID:   sql.NullInt64{Int64: issueID, Valid: issueID != 0},
		Name:      "ws",
		Path:      "/ws",
		Status:    status,
		OwnerType: "user",
		OwnerID:   1,
	})
	require.NoError(t, err)
	return ws.ID
}

// addCost records a workspace cost so SumEpicSubtreeCostUSD accumulates it.
func (e *epicTestEnv) addCost(t *testing.T, workspaceID int64, usd float64) {
	t.Helper()
	require.NoError(t, e.q.CreateWorkspaceCost(e.ctx, store.CreateWorkspaceCostParams{
		WorkspaceID: workspaceID,
		CostUsd:     usd,
	}))
}

// makeStandalone creates a standalone (no-parent) task issue.
func (e *epicTestEnv) makeStandalone(t *testing.T, columnID int64, title string) int64 {
	t.Helper()
	d, err := e.kanban.CreateIssue(e.ctx, columnID, title, "", 0, 0, "", "", "", 0, nil, "task", 0, nil, nil, 0)
	require.NoError(t, err)
	return d.ID
}

func TestGuard_CheckFanOutBatch(t *testing.T) {
	ctx := context.Background()
	g := service.NewOrchestrationGuard(nil, service.OrchestrationLimits{MaxBatchIssues: 20})
	assert.NoError(t, g.CheckFanOutBatch(ctx, 20))
	assert.Error(t, g.CheckFanOutBatch(ctx, 21))

	// Disabled (<=0) never rejects.
	gOff := service.NewOrchestrationGuard(nil, service.OrchestrationLimits{MaxBatchIssues: 0})
	assert.NoError(t, gOff.CheckFanOutBatch(ctx, 1000))

	// Nil guard never rejects.
	var gNil *service.OrchestrationGuard
	assert.NoError(t, gNil.CheckFanOutBatch(ctx, 1000))
}

func TestGuard_AdmitConcurrencyQueue(t *testing.T) {
	e := setupEpicTest(t)
	col := e.makeProjectColumn(t)
	g := service.NewOrchestrationGuard(e.q, service.OrchestrationLimits{MaxConcurrentWorkspaces: 1})

	// One active workspace already occupies the single slot.
	a := e.makeStandalone(t, col, "A")
	e.addWorkspace(t, a, "created")

	b := e.makeStandalone(t, col, "B")
	dB, err := g.AdmitWorkspace(e.ctx, b)
	require.NoError(t, err)
	assert.Equal(t, service.GuardQueue, dB.Action)
	assert.Equal(t, 1, dB.QueuePosition)

	c := e.makeStandalone(t, col, "C")
	dC, err := g.AdmitWorkspace(e.ctx, c)
	require.NoError(t, err)
	assert.Equal(t, service.GuardQueue, dC.Action)
	assert.Equal(t, 2, dC.QueuePosition)

	// Re-dispatching B does not enqueue a duplicate; position stays 2.
	dB2, err := g.AdmitWorkspace(e.ctx, b)
	require.NoError(t, err)
	assert.Equal(t, service.GuardQueue, dB2.Action)
	assert.Equal(t, 2, dB2.QueuePosition)
}

func TestGuard_AdmitUnderConcurrency(t *testing.T) {
	e := setupEpicTest(t)
	col := e.makeProjectColumn(t)
	g := service.NewOrchestrationGuard(e.q, service.OrchestrationLimits{MaxConcurrentWorkspaces: 2})

	a := e.makeStandalone(t, col, "A")
	e.addWorkspace(t, a, "created") // 1 active, cap 2 -> still room

	b := e.makeStandalone(t, col, "B")
	d, err := g.AdmitWorkspace(e.ctx, b)
	require.NoError(t, err)
	assert.Equal(t, service.GuardAdmit, d.Action)

	// A completed workspace does not occupy a slot.
	x := e.makeStandalone(t, col, "X")
	e.addWorkspace(t, x, "completed")
	d2, err := g.AdmitWorkspace(e.ctx, b)
	require.NoError(t, err)
	assert.Equal(t, service.GuardAdmit, d2.Action)
}

func TestGuard_AdmitChainBudget(t *testing.T) {
	e := setupEpicTest(t)
	col := e.makeProjectColumn(t)
	// Concurrency disabled so this isolates the cost gate.
	limits := service.OrchestrationLimits{ChainCostBudgetUSD: 10.0, ChainCostWarnRatio: 0.8}
	g := service.NewOrchestrationGuard(e.q, limits)

	epic := e.makeEpic(t, col, "Epic")
	child := e.makeChild(t, col, epic, 0, "child")
	ws := e.addWorkspace(t, epic, "completed")

	// Spend 9.5 of a 10 budget -> admit + near-budget warning.
	e.addCost(t, ws, 9.5)
	d, err := g.AdmitWorkspace(e.ctx, child)
	require.NoError(t, err)
	assert.Equal(t, service.GuardAdmit, d.Action)
	assert.NotEmpty(t, d.Warn, "should warn near budget")
	assert.InDelta(t, 9.5, d.SpentUSD, 0.001)

	// Push over budget -> block.
	e.addCost(t, ws, 1.5) // total 11.0
	d2, err := g.AdmitWorkspace(e.ctx, child)
	require.NoError(t, err)
	assert.Equal(t, service.GuardBlock, d2.Action)
	assert.NotEmpty(t, d2.Reason)
}

// A live settings override lowers the budget below the static limits and must
// take effect on the next decision (the no-restart path).
func TestGuard_SettingsBudgetOverrideBlocks(t *testing.T) {
	e := setupEpicTest(t)
	col := e.makeProjectColumn(t)

	// Static limits would allow (budget 100); settings override lowers it to 1.
	g := service.NewOrchestrationGuard(e.q, service.OrchestrationLimits{ChainCostBudgetUSD: 100})
	ss := service.NewServerSettingsService(store.Wrap(e.db))
	ss.SetCacheTTL(0)
	require.NoError(t, ss.Put(e.ctx, service.KeyOrchBudgetUSD, "1", 0))
	g.SetSettings(service.NewOrchestrationSettings(ss, 100, 5, 20, 80))

	epic := e.makeEpic(t, col, "Epic")
	child := e.makeChild(t, col, epic, 0, "child")
	ws := e.addWorkspace(t, epic, "completed")
	e.addCost(t, ws, 2.0) // $2 spent, over the $1 override

	d, err := g.AdmitWorkspace(e.ctx, child)
	require.NoError(t, err)
	assert.Equal(t, service.GuardBlock, d.Action, "budget override $1 exceeded -> block")
	assert.InDelta(t, 1.0, d.BudgetUSD, 0.001, "decision reflects overridden budget")
}

func TestGuard_StandaloneIgnoresChainBudget(t *testing.T) {
	e := setupEpicTest(t)
	col := e.makeProjectColumn(t)
	g := service.NewOrchestrationGuard(e.q, service.OrchestrationLimits{ChainCostBudgetUSD: 1.0, ChainCostWarnRatio: 0.5})

	// A standalone issue with an expensive workspace is not part of any tree.
	a := e.makeStandalone(t, col, "A")
	ws := e.addWorkspace(t, a, "completed")
	e.addCost(t, ws, 99.0)

	d, err := g.AdmitWorkspace(e.ctx, a)
	require.NoError(t, err)
	assert.Equal(t, service.GuardAdmit, d.Action)
	assert.Empty(t, d.Warn)
}

func TestGuard_OnSlotFreedDrains(t *testing.T) {
	e := setupEpicTest(t)
	col := e.makeProjectColumn(t)
	g := service.NewOrchestrationGuard(e.q, service.OrchestrationLimits{MaxConcurrentWorkspaces: 1})
	starter := &fakeStarter{}
	g.SetStarter(starter)

	// Fill the slot, queue B behind it.
	a := e.makeStandalone(t, col, "A")
	wsA := e.addWorkspace(t, a, "created")
	b := e.makeStandalone(t, col, "B")
	dB, err := g.AdmitWorkspace(e.ctx, b)
	require.NoError(t, err)
	require.Equal(t, service.GuardQueue, dB.Action)

	// Complete A's workspace -> a slot frees.
	_, err = e.db.Exec("UPDATE workspaces SET status='completed' WHERE id=?", wsA)
	require.NoError(t, err)

	g.OnSlotFreed(e.ctx, "user", 1)

	starter.mu.Lock()
	started := append([]int64(nil), starter.started...)
	starter.mu.Unlock()
	assert.Equal(t, []int64{b}, started, "queued B should have been started")

	// Queue entry is now marked started (no longer counted as queued).
	cnt, err := e.q.CountQueuedForOwner(e.ctx, store.CountQueuedForOwnerParams{OwnerType: "user", OwnerID: 1})
	require.NoError(t, err)
	assert.Equal(t, int64(0), cnt)
}

// TestGuard_OnSlotFreedDrainsWhenUnlimited reproduces the deadlock where an issue
// enqueued while the concurrency cap was finite stays queued forever after the cap
// is raised to "unlimited" (<=0): the drain used to early-return on maxConc<=0, so
// the stranded entry was never started. The drain must instead treat <=0 as "no cap"
// and drain the whole queue.
func TestGuard_OnSlotFreedDrainsWhenUnlimited(t *testing.T) {
	e := setupEpicTest(t)
	col := e.makeProjectColumn(t)
	// Cap is unlimited (0). An entry sits in the queue from an earlier finite-cap
	// period (simulated by enqueuing directly).
	g := service.NewOrchestrationGuard(e.q, service.OrchestrationLimits{MaxConcurrentWorkspaces: 0})
	starter := &fakeStarter{}
	g.SetStarter(starter)

	b := e.makeStandalone(t, col, "B")
	require.NoError(t, e.q.EnqueueWorkspaceStart(e.ctx, store.EnqueueWorkspaceStartParams{
		OwnerType: "user", OwnerID: 1, IssueID: b,
	}))

	g.OnSlotFreed(e.ctx, "user", 1)

	starter.mu.Lock()
	started := append([]int64(nil), starter.started...)
	starter.mu.Unlock()
	assert.Equal(t, []int64{b}, started, "stranded queued entry must drain once the cap is unlimited")

	cnt, err := e.q.CountQueuedForOwner(e.ctx, store.CountQueuedForOwnerParams{OwnerType: "user", OwnerID: 1})
	require.NoError(t, err)
	assert.Equal(t, int64(0), cnt, "queue must be empty after draining")
}

// TestGuard_DrainAllQueuedOwners covers the cap-raised path: a global drain must
// start every owner's stranded queue entries without needing a per-owner slot-free
// event. Here the cap is unlimited, so every queued entry should drain.
func TestGuard_DrainAllQueuedOwners(t *testing.T) {
	e := setupEpicTest(t)
	col := e.makeProjectColumn(t)
	g := service.NewOrchestrationGuard(e.q, service.OrchestrationLimits{MaxConcurrentWorkspaces: 0})
	starter := &fakeStarter{}
	g.SetStarter(starter)

	a := e.makeStandalone(t, col, "A")
	b := e.makeStandalone(t, col, "B")
	for _, id := range []int64{a, b} {
		require.NoError(t, e.q.EnqueueWorkspaceStart(e.ctx, store.EnqueueWorkspaceStartParams{
			OwnerType: "user", OwnerID: 1, IssueID: id,
		}))
	}

	g.DrainAllQueuedOwners(e.ctx)

	starter.mu.Lock()
	started := append([]int64(nil), starter.started...)
	starter.mu.Unlock()
	assert.ElementsMatch(t, []int64{a, b}, started, "all queued entries across the owner must drain")

	cnt, err := e.q.CountQueuedForOwner(e.ctx, store.CountQueuedForOwnerParams{OwnerType: "user", OwnerID: 1})
	require.NoError(t, err)
	assert.Equal(t, int64(0), cnt)
}
