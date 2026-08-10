package service_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// fakeWorkspaceCreator records every Create call and returns a synthetic
// workspace with an incrementing ID and the requested issue id. It never
// touches disk, so project repos are irrelevant.
type fakeWorkspaceCreator struct {
	mu       sync.Mutex
	nextID   int64
	calls    []service.CreateWorkspaceInput
	createdC []int64           // issue ids of created workspaces, in order
	env      map[string]string // last SetWorkspaceEnv values (key->value)
	// onCreated mirrors WorkspaceService's post-create hook so tests exercise the
	// real auto-linkage path (OnWorkspaceCreated). Wired in setupEpicTest.
	onCreated func(context.Context, store.Workspace)
}

func (f *fakeWorkspaceCreator) Create(ctx context.Context, input service.CreateWorkspaceInput) (*service.WorkspaceResult, error) {
	f.mu.Lock()
	f.nextID++
	f.calls = append(f.calls, input)
	var issueID int64
	if input.IssueID != nil {
		issueID = *input.IssueID
		f.createdC = append(f.createdC, issueID)
	}
	ws := store.Workspace{ID: f.nextID, Path: "/ws", IssueID: sql.NullInt64{Int64: issueID, Valid: input.IssueID != nil}}
	hook := f.onCreated
	f.mu.Unlock()
	if hook != nil {
		hook(ctx, ws)
	}
	return &service.WorkspaceResult{Workspace: ws}, nil
}

func (f *fakeWorkspaceCreator) SetWorkspaceEnv(_ context.Context, _ int64, key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.env == nil {
		f.env = map[string]string{}
	}
	f.env[key] = value
	return nil
}

// fakeCompleter records RequestWorkspaceCompletion calls. To mirror production
// (where finalize sets the issue lifecycle completed and publishes
// workspace_completed -> the engine advances), the test wires its complete func
// to set the issue completed and drive OnWorkspaceCompleted directly. It models a
// no-floor-gate project: completion finalizes synchronously.
type fakeCompleter struct {
	done     []int64
	complete func(workspaceID int64)
}

func (f *fakeCompleter) RequestWorkspaceCompletion(_ context.Context, workspaceID int64, _ service.CompletionTrigger) (service.RequestCompletionResult, error) {
	f.done = append(f.done, workspaceID)
	if f.complete != nil {
		f.complete(workspaceID)
	}
	return service.RequestCompletionResult{Status: service.CompletionFinalized}, nil
}

// fakeAgentProxy / fakeAgentSession record kickoff messages so tests can assert
// the engine sends the issue title + description to each dispatched child.
type fakeAgentSession struct{ kickoffs *[]string }

func (s *fakeAgentSession) SetActiveRunID(int64)         {}
func (s *fakeAgentSession) Cancel(context.Context) error { return nil }
func (s *fakeAgentSession) SendKickoff(_ context.Context, _, msg, _ string) {
	*s.kickoffs = append(*s.kickoffs, msg)
}

type fakeAgentProxy struct {
	kickoffs []string
	enqueued []string
	// runningWS, if non-zero, Deliver queues the message instead of kicking off.
	runningWS int64
}

func (p *fakeAgentProxy) GetOrStartSession(_ context.Context, _ int64, _ int64) (service.AgentSession, error) {
	return &fakeAgentSession{kickoffs: &p.kickoffs}, nil
}
func (p *fakeAgentProxy) GetSession(int64) service.AgentSession  { return nil }
func (p *fakeAgentProxy) PrepareUserSend(context.Context, int64) {}
func (p *fakeAgentProxy) Deliver(_ context.Context, wsID int64, _, content, _ string) (bool, int64, error) {
	if p.runningWS != 0 && p.runningWS == wsID {
		p.enqueued = append(p.enqueued, content)
		return true, int64(len(p.enqueued)), nil
	}
	p.kickoffs = append(p.kickoffs, content)
	return false, 0, nil
}

func (f *fakeWorkspaceCreator) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeWorkspaceCreator) issueIDs() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int64, len(f.createdC))
	copy(out, f.createdC)
	return out
}

// epicTestEnv bundles the engine + a kanban service over an in-memory DB.
type epicTestEnv struct {
	svc    *service.EpicExecutionService
	kanban *service.KanbanService
	fake   *fakeWorkspaceCreator
	db     *sql.DB
	q      *store.Queries
	ctx    context.Context
}

func setupEpicTest(t *testing.T) *epicTestEnv {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(store.Schema)
	require.NoError(t, err)
	store.Migrate(db)

	q := store.New(db)
	activitySvc := service.NewIssueActivityService(q)
	kanban := service.NewKanbanService(db, q, activitySvc, nil, nil)
	fake := &fakeWorkspaceCreator{}
	// bus nil: Start is a no-op; tests drive OnWorkspaceCompleted directly.
	svc := service.NewEpicExecutionService(db, q, kanban, fake, nil)
	// Mirror production: WorkspaceService.Create invokes the engine's post-create
	// hook, so a created workspace is auto-linked by its issue type.
	fake.onCreated = svc.OnWorkspaceCreated

	return &epicTestEnv{svc: svc, kanban: kanban, fake: fake, db: db, q: q, ctx: context.Background()}
}

// makeWorkspace inserts a workspace row linked to issueID and returns its id.
// Used by OnAgentDone tests, which resolve workspace -> issue via the DB.
func (e *epicTestEnv) makeWorkspace(t *testing.T, issueID int64) int64 {
	t.Helper()
	ws, err := e.q.CreateWorkspace(e.ctx, store.CreateWorkspaceParams{
		IssueID:   sql.NullInt64{Int64: issueID, Valid: true},
		Name:      "ws",
		Path:      "/ws",
		Status:    "needs_review",
		OwnerType: "user",
		OwnerID:   1,
	})
	require.NoError(t, err)
	return ws.ID
}

// makeColumn creates a project + a single column and returns the column id.
func (e *epicTestEnv) makeProjectColumn(t *testing.T) int64 {
	t.Helper()
	p, err := e.q.CreateProject(e.ctx, store.CreateProjectParams{Name: "epic-proj", OwnerType: "user", OwnerID: 1})
	require.NoError(t, err)
	col, err := e.q.CreateColumn(e.ctx, store.CreateColumnParams{ProjectID: p.ID, Name: "Backlog", Position: 0})
	require.NoError(t, err)
	return col.ID
}

// makeEpic creates an epic issue in the column and returns its id.
func (e *epicTestEnv) makeEpic(t *testing.T, columnID int64, title string) int64 {
	t.Helper()
	d, err := e.kanban.CreateIssue(e.ctx, columnID, title, "", 0, 0, "", "", "", 0, nil, "epic", 0, nil, nil, 0)
	require.NoError(t, err)
	return d.ID
}

// makeChild creates a child task under the epic with the given wave.
func (e *epicTestEnv) makeChild(t *testing.T, columnID, epicID, wave int64, title string) int64 {
	t.Helper()
	d, err := e.kanban.CreateIssue(e.ctx, columnID, title, "", 0, 0, "", "", "", 0, &epicID, "task", wave, nil, nil, 0)
	require.NoError(t, err)
	return d.ID
}

func (e *epicTestEnv) execStatus(t *testing.T, issueID int64) string {
	t.Helper()
	iss, err := e.q.GetIssue(e.ctx, issueID)
	require.NoError(t, err)
	return iss.ExecStatus
}

// markChildLifecycleCompleted simulates the workspace mark-done side-effect
// that also flips the linked issue lifecycle to completed (so wave-resolution
// counting matches production). The engine itself sets exec_status.
func (e *epicTestEnv) markChildLifecycleCompleted(t *testing.T, issueID int64) {
	t.Helper()
	_, err := e.kanban.UpdateIssueLifecycle(e.ctx, issueID, "completed")
	require.NoError(t, err)
}

func TestEpicExecution_GetEpicProgress(t *testing.T) {
	e := setupEpicTest(t)
	colID := e.makeProjectColumn(t)
	epicID := e.makeEpic(t, colID, "Progress Epic")
	c0 := e.makeChild(t, colID, epicID, 0, "c0")
	_ = e.makeChild(t, colID, epicID, 0, "c1")

	done, total, status, err := e.svc.GetEpicProgress(e.ctx, epicID)
	require.NoError(t, err)
	assert.Equal(t, 0, done)
	assert.Equal(t, 2, total)
	assert.Equal(t, "idle", status)

	e.markChildLifecycleCompleted(t, c0)
	done, total, _, err = e.svc.GetEpicProgress(e.ctx, epicID)
	require.NoError(t, err)
	assert.Equal(t, 1, done)
	assert.Equal(t, 2, total)
}

// ─── Orchestration via the create-workspace hook (OnWorkspaceCreated) ──────────

func TestEpicExecution_OnWorkspaceCreated_EpicBecomesOrchestrator(t *testing.T) {
	e := setupEpicTest(t)
	proxy := &fakeAgentProxy{}
	e.svc.SetAgentProxy(proxy)
	colID := e.makeProjectColumn(t)
	epicID := e.makeEpic(t, colID, "Ship login")
	_ = e.makeChild(t, colID, epicID, 0, "Backend")
	_ = e.makeChild(t, colID, epicID, 1, "Frontend")

	// Simulate creating a workspace ON the epic issue (manual or auto) -> the
	// post-create hook auto-links it as an orchestration workspace.
	e.svc.OnWorkspaceCreated(e.ctx, store.Workspace{ID: 999, Path: "/ws/epic", IssueID: sql.NullInt64{Int64: epicID, Valid: true}})

	assert.Equal(t, "running", e.execStatus(t, epicID), "epic marked running by orchestration ws")
	require.Len(t, proxy.kickoffs, 1, "orchestration agent kicked off once")
	msg := proxy.kickoffs[0]
	assert.Contains(t, msg, "Ship login")
	assert.Contains(t, msg, "start_workspace")
	assert.Contains(t, msg, "Backend")
	assert.Contains(t, msg, "Frontend")

	// Event-driven orchestration: the watchdog continue-poll is disabled (budget 0)
	// and the kickoff spells out the no-polling contract + the stop sentinel.
	assert.Equal(t, "autohost", e.fake.env["NIUNIU_PERMISSION_MODE"])
	assert.Equal(t, "0", e.fake.env["NIUNIU_AUTOHOST_BUDGET"], "orchestration ws must not auto-continue-poll")
	assert.Contains(t, msg, "不要轮询")
	assert.Contains(t, msg, "[AUTOHOST_DONE]")
}

// --- 事件驱动的子任务完成检查 (epic 自动执行逻辑优化, ws-568) ---

// A child reaching terminal success notifies the epic's orchestration agent once,
// with the sibling status list attached; a duplicate completion event does not
// re-notify (success notifications are edge-triggered on exec_status).
func TestEpicExecution_ChildCompletionNotifiesOrchestrator(t *testing.T) {
	e := setupEpicTest(t)
	proxy := &fakeAgentProxy{}
	e.svc.SetAgentProxy(proxy)
	colID := e.makeProjectColumn(t)
	epicID := e.makeEpic(t, colID, "Epic Notify")
	c0 := e.makeChild(t, colID, epicID, 0, "child-zero")
	_ = e.makeChild(t, colID, epicID, 1, "child-one")
	_ = e.makeWorkspace(t, epicID) // active orchestration workspace to wake
	require.NoError(t, e.q.SetIssueExecStatus(e.ctx, store.SetIssueExecStatusParams{ExecStatus: "running", ID: c0}))

	e.svc.OnWorkspaceCompleted(e.ctx, c0, true)

	assert.Equal(t, "done", e.execStatus(t, c0))
	require.Len(t, proxy.kickoffs, 1, "orchestrator notified once on child completion")
	msg := proxy.kickoffs[0]
	assert.Contains(t, msg, "【子任务事件】")
	assert.Contains(t, msg, "child-zero")
	assert.Contains(t, msg, "child-one", "sibling status list attached so the agent skips a board read")
	assert.Contains(t, msg, "start_workspace")
	assert.Contains(t, msg, "[AUTOHOST_DONE]")
	assert.Contains(t, msg, "不要轮询")

	e.svc.OnWorkspaceCompleted(e.ctx, c0, true)
	assert.Len(t, proxy.kickoffs, 1, "duplicate completion event does not re-notify")
}

// A child failing (workspace torn down without completing) also notifies the
// orchestration agent so it can decide retry / abandon — failures are never
// suppressed.
func TestEpicExecution_ChildFailureNotifiesOrchestrator(t *testing.T) {
	e := setupEpicTest(t)
	proxy := &fakeAgentProxy{}
	e.svc.SetAgentProxy(proxy)
	colID := e.makeProjectColumn(t)
	epicID := e.makeEpic(t, colID, "Epic NotifyFail")
	c0 := e.makeChild(t, colID, epicID, 0, "child-zero")
	_ = e.makeWorkspace(t, epicID)
	require.NoError(t, e.q.SetIssueExecStatus(e.ctx, store.SetIssueExecStatusParams{ExecStatus: "running", ID: c0}))

	e.svc.OnWorkspaceCompleted(e.ctx, c0, false)

	assert.Equal(t, "failed", e.execStatus(t, c0))
	require.Len(t, proxy.kickoffs, 1)
	assert.Contains(t, proxy.kickoffs[0], "失败")
	assert.Contains(t, proxy.kickoffs[0], "child-zero")
}

// With the continue budget disabled, the orchestration agent's turn ending while
// children are still pending is an idle pause awaiting child events — OnAgentDone
// must NOT run the finalize/confirm path.
func TestEpicExecution_OnAgentDone_EpicWaitsForPendingChildren(t *testing.T) {
	e := setupEpicTest(t)
	colID := e.makeProjectColumn(t)
	epicID := e.makeEpic(t, colID, "Epic Waits")
	_ = e.makeChild(t, colID, epicID, 0, "c0") // pending: lifecycle open, exec idle
	require.NoError(t, e.q.SetIssueExecStatus(e.ctx, store.SetIssueExecStatusParams{ExecStatus: "running", ID: epicID}))

	comp := &fakeCompleter{}
	e.svc.SetCompleter(comp)
	conf := &fakeReviewConfirmer{confirm: true}
	e.svc.SetReviewConfirmer(conf)

	wsID := e.makeWorkspace(t, epicID)
	e.svc.OnAgentDone(e.ctx, wsID)

	assert.Empty(t, comp.done, "epic not completed while children pending")
	assert.Equal(t, 0, conf.count(), "user not asked to confirm while children pending")
	assert.Equal(t, "running", e.execStatus(t, epicID), "epic stays running, awaiting child events")
}

// Once every child is terminal, the next agent_done proceeds to the normal
// review-confirm/finalize path.
func TestEpicExecution_OnAgentDone_EpicProceedsWhenChildrenTerminal(t *testing.T) {
	e := setupEpicTest(t)
	colID := e.makeProjectColumn(t)
	epicID := e.makeEpic(t, colID, "Epic Proceeds")
	c0 := e.makeChild(t, colID, epicID, 0, "c0")
	e.markChildLifecycleCompleted(t, c0)
	require.NoError(t, e.q.SetIssueExecStatus(e.ctx, store.SetIssueExecStatusParams{ExecStatus: "running", ID: epicID}))

	doneCh := make(chan int64, 1)
	comp := &fakeCompleter{complete: func(wsID int64) {
		e.svc.OnWorkspaceCompleted(e.ctx, epicID, true)
		doneCh <- wsID
	}}
	e.svc.SetCompleter(comp)
	conf := &fakeReviewConfirmer{confirm: true}
	e.svc.SetReviewConfirmer(conf)

	wsID := e.makeWorkspace(t, epicID)
	e.svc.OnAgentDone(e.ctx, wsID)

	select {
	case got := <-doneCh:
		assert.Equal(t, wsID, got)
	case <-time.After(2 * time.Second):
		t.Fatal("epic was not completed after all children terminal")
	}
	assert.Equal(t, 1, conf.count(), "user asked once when orchestration genuinely finished")
	assert.Equal(t, "done", e.execStatus(t, epicID))
}

func TestEpicExecution_OnWorkspaceCreated_ChildKickedOff(t *testing.T) {
	e := setupEpicTest(t)
	proxy := &fakeAgentProxy{}
	e.svc.SetAgentProxy(proxy)
	colID := e.makeProjectColumn(t)
	epicID := e.makeEpic(t, colID, "Epic")
	child, err := e.kanban.CreateIssue(e.ctx, colID, "Build API", "Use JWT.", 0, 0, "", "", "", 0, &epicID, "task", 0, nil, nil, 0)
	require.NoError(t, err)

	e.svc.OnWorkspaceCreated(e.ctx, store.Workspace{ID: 1, Path: "/ws/c", IssueID: sql.NullInt64{Int64: child.ID, Valid: true}})
	require.Len(t, proxy.kickoffs, 1)
	assert.Equal(t, "# Build API\n\nUse JWT.", proxy.kickoffs[0])
}

func TestEpicExecution_OnWorkspaceCreated_StandaloneNoKickoff(t *testing.T) {
	e := setupEpicTest(t)
	proxy := &fakeAgentProxy{}
	e.svc.SetAgentProxy(proxy)
	colID := e.makeProjectColumn(t)
	// Standalone issue: no parent, not an epic -> no auto-kickoff (manual control).
	solo, err := e.kanban.CreateIssue(e.ctx, colID, "solo", "", 0, 0, "", "", "", 0, nil, "", 0, nil, nil, 0)
	require.NoError(t, err)
	e.svc.OnWorkspaceCreated(e.ctx, store.Workspace{ID: 1, Path: "/ws/s", IssueID: sql.NullInt64{Int64: solo.ID, Valid: true}})
	assert.Empty(t, proxy.kickoffs)
	assert.Equal(t, "idle", e.execStatus(t, solo.ID))
}

// A manually-created standalone workspace gets no autohost kickoff, but DOES get
// the board menu injected into its agent instruction file — so the (bypass-
// permissions) agent knows the board's processing stages and can advance_issue.
func TestEpicExecution_OnWorkspaceCreated_StandaloneInjectsBoardMenu(t *testing.T) {
	e := setupEpicTest(t)
	proxy := &fakeAgentProxy{}
	e.svc.SetAgentProxy(proxy)
	colID := e.makeProjectColumn(t)
	solo, err := e.kanban.CreateIssue(e.ctx, colID, "solo", "", 0, 0, "", "", "", 0, nil, "", 0, nil, nil, 0)
	require.NoError(t, err)

	wsPath := t.TempDir()
	e.svc.OnWorkspaceCreated(e.ctx, store.Workspace{ID: 1, Path: wsPath, IssueID: sql.NullInt64{Int64: solo.ID, Valid: true}})

	// Manual control preserved: no kickoff, status untouched.
	assert.Empty(t, proxy.kickoffs)
	assert.Equal(t, "idle", e.execStatus(t, solo.ID))

	// Board menu injected into CLAUDE.md (Claude default), wrapped in BOARD markers.
	data, err := os.ReadFile(filepath.Join(wsPath, "CLAUDE.md"))
	require.NoError(t, err, "standalone workspace should have CLAUDE.md with the board menu")
	assert.Contains(t, string(data), "<!-- BOARD:START -->")
	assert.Contains(t, string(data), "看板处理阶段")
}

func TestEpicExecution_OrchestrationCompletionWritesBack(t *testing.T) {
	e := setupEpicTest(t)
	colID := e.makeProjectColumn(t)
	epicID := e.makeEpic(t, colID, "Epic done")
	_ = e.makeChild(t, colID, epicID, 0, "c0")
	require.NoError(t, e.q.SetIssueExecStatus(e.ctx, store.SetIssueExecStatusParams{ExecStatus: "running", ID: epicID}))

	// The orchestration workspace (bound to the epic issue) completing writes the
	// result back to the epic's exec_status.
	e.svc.OnWorkspaceCompleted(e.ctx, epicID, true)
	assert.Equal(t, "done", e.execStatus(t, epicID))
}

func TestEpicExecution_OnAgentDone_IgnoresStandalone(t *testing.T) {
	e := setupEpicTest(t)
	comp := &fakeCompleter{}
	e.svc.SetCompleter(comp)
	colID := e.makeProjectColumn(t)
	solo, err := e.kanban.CreateIssue(e.ctx, colID, "solo", "", 0, 0, "", "", "", 0, nil, "", 0, nil, nil, 0)
	require.NoError(t, err)
	wsID := e.makeWorkspace(t, solo.ID)

	e.svc.OnAgentDone(e.ctx, wsID)
	assert.Empty(t, comp.done, "standalone workspace not auto-completed (human review preserved)")
}

// TestEpicExecution_BusEventDrivesWorkerHandler verifies the decoupled event
// path: a workspace_completed published on the SHARED bus is picked up by the hot
// consumer and executed by the worker goroutine (off the bus path), advancing the
// state machine without a direct handler call. Guards the fix for the freeze where
// slow git work on the consumer goroutine caused the bus buffer to overflow and
// drop terminal events.
func TestEpicExecution_BusEventDrivesWorkerHandler(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(store.Schema)
	require.NoError(t, err)
	store.Migrate(db)
	q := store.New(db)
	kanban := service.NewKanbanService(db, q, service.NewIssueActivityService(q), nil, nil)
	fake := &fakeWorkspaceCreator{}
	bus := event.NewBus()
	svc := service.NewEpicExecutionService(db, q, kanban, fake, bus)
	fake.onCreated = svc.OnWorkspaceCreated
	svc.Start()
	t.Cleanup(svc.Stop)

	e := &epicTestEnv{svc: svc, kanban: kanban, fake: fake, db: db, q: q, ctx: context.Background()}
	colID := e.makeProjectColumn(t)
	epicID := e.makeEpic(t, colID, "Epic Bus")
	c0 := e.makeChild(t, colID, epicID, 0, "c0")
	// A child dispatched by the orchestration agent is 'running'.
	require.NoError(t, q.SetIssueExecStatus(e.ctx, store.SetIssueExecStatusParams{ExecStatus: "running", ID: c0}))

	content, _ := json.Marshal(event.WorkspaceCompletedEvent{IssueID: c0, Success: true})
	bus.Publish(event.OutputEvent{Type: event.EventWorkspaceCompleted, Content: string(content)})

	// The worker (not a direct call) must drive OnWorkspaceCompleted -> child 收口 'done'.
	require.Eventually(t, func() bool {
		iss, err := q.GetIssue(context.Background(), c0)
		return err == nil && iss.ExecStatus == "done"
	}, 2*time.Second, 10*time.Millisecond, "bus event should reach the worker and mark the child done")
}

func TestEpicExecution_StartWorkspaceForIssueIsIdempotent(t *testing.T) {
	e := setupEpicTest(t)
	colID := e.makeProjectColumn(t)
	d, err := e.kanban.CreateIssue(e.ctx, colID, "solo", "", 0, 0, "", "", "", 0, nil, "", 0, nil, nil, 0)
	require.NoError(t, err)
	existing := e.makeWorkspace(t, d.ID)

	out, err := e.svc.StartWorkspaceForIssue(e.ctx, d.ID)
	require.NoError(t, err)
	assert.Equal(t, existing, out.WorkspaceID, "returns the existing workspace id")
	assert.Equal(t, 0, e.fake.count(), "no new workspace created on reuse")
}

func TestEpicExecution_StartWorkspaceForIssue(t *testing.T) {
	e := setupEpicTest(t)
	colID := e.makeProjectColumn(t)
	// A plain top-level issue (no epic) can still be dispatched.
	d, err := e.kanban.CreateIssue(e.ctx, colID, "solo", "", 0, 0, "", "", "", 0, nil, "", 0, nil, nil, 0)
	require.NoError(t, err)
	out, err := e.svc.StartWorkspaceForIssue(e.ctx, d.ID)
	require.NoError(t, err)
	assert.NotZero(t, out.WorkspaceID)
	assert.False(t, out.Queued)
	assert.Equal(t, []int64{d.ID}, e.fake.issueIDs())
}

// TestEpicExecution_ChildInheritsEpicLanguage verifies that a child workspace
// dispatched under an epic inherits the language persisted on the epic's own
// workspace — the headline of the persist+inherit change. Epic children are
// created async (no originating HTTP request carrying the user's UI language),
// so without inheritance they would fall back to the generic directive.
func TestEpicExecution_ChildInheritsEpicLanguage(t *testing.T) {
	e := setupEpicTest(t)
	colID := e.makeProjectColumn(t)
	epicID := e.makeEpic(t, colID, "epic")
	childID := e.makeChild(t, colID, epicID, 0, "child")

	// Seed the epic's own workspace with a language, mirroring a control
	// workspace created via the normal, header-carrying create flow.
	_, err := e.q.CreateWorkspace(e.ctx, store.CreateWorkspaceParams{
		IssueID:   sql.NullInt64{Int64: epicID, Valid: true},
		Name:      "epic-ws",
		Path:      "/epic-ws",
		Status:    "created",
		OwnerType: "user",
		OwnerID:   1,
		Language:  "zh-CN",
	})
	require.NoError(t, err)

	// Dispatching the child runs createWorkspaceForIssue, which must read the
	// epic workspace's language and forward it on the child create.
	_, err = e.svc.StartWorkspaceForIssue(e.ctx, childID)
	require.NoError(t, err)

	require.NotEmpty(t, e.fake.calls, "child workspace should have been created")
	last := e.fake.calls[len(e.fake.calls)-1]
	require.NotNil(t, last.IssueID)
	require.Equal(t, childID, *last.IssueID)
	require.Equal(t, "zh-CN", last.Language, "child must inherit the epic's language")
}

// TestEpicExecution_ChildNoEpicLanguageStaysGeneric verifies the documented
// edge case: when the epic's workspace carries no language (e.g. it was itself
// created via an async path with no UI-language context), children inherit the
// empty value and thus fall back to the generic directive — no crash, no stale
// default.
func TestEpicExecution_ChildNoEpicLanguageStaysGeneric(t *testing.T) {
	e := setupEpicTest(t)
	colID := e.makeProjectColumn(t)
	epicID := e.makeEpic(t, colID, "epic")
	childID := e.makeChild(t, colID, epicID, 0, "child")

	// Epic workspace with no language (default '').
	e.makeWorkspace(t, epicID)

	_, err := e.svc.StartWorkspaceForIssue(e.ctx, childID)
	require.NoError(t, err)

	require.NotEmpty(t, e.fake.calls)
	last := e.fake.calls[len(e.fake.calls)-1]
	require.Equal(t, "", last.Language, "no epic language → empty (generic directive)")
}

func TestEpicExecution_StartWorkspaceQueuedAtCap(t *testing.T) {
	e := setupEpicTest(t)
	colID := e.makeProjectColumn(t)
	// Cap concurrency at 1 and pre-fill the slot with an active workspace.
	guard := service.NewOrchestrationGuard(e.q, service.OrchestrationLimits{MaxConcurrentWorkspaces: 1})
	e.svc.SetGuard(guard)
	occupant, err := e.kanban.CreateIssue(e.ctx, colID, "occupant", "", 0, 0, "", "", "", 0, nil, "", 0, nil, nil, 0)
	require.NoError(t, err)
	e.addWorkspace(t, occupant.ID, "created")

	d, err := e.kanban.CreateIssue(e.ctx, colID, "queued", "", 0, 0, "", "", "", 0, nil, "", 0, nil, nil, 0)
	require.NoError(t, err)
	out, err := e.svc.StartWorkspaceForIssue(e.ctx, d.ID)
	require.NoError(t, err)
	assert.True(t, out.Queued, "should queue at the concurrency cap")
	assert.Equal(t, 1, out.QueuePosition)
	assert.Equal(t, 0, e.fake.count(), "no workspace created while queued")
}

// ─── Phase 2: review phase, merge-to-main, child branch integration ───────────

// errMergeConflict simulates a real merge conflict for the conflict-path test.
var errMergeConflict = errors.New("merge conflict")

// epicBranch mirrors the engine's deterministic epic feature-branch name.
func epicBranch(epicID int64) string { return "epic/" + strconv.FormatInt(epicID, 10) }

// fakeMerger records CommitWorktree / MergeWorktree calls and can be configured
// to fail the merge (simulating a conflict) for a given worktree name.
type fakeMerger struct {
	commits   []string
	merges    []string // "name->branch"
	syncs     []string // "name<-branch" (epic branch synced back into the epic workspace)
	mergeFail map[string]error
}

func (f *fakeMerger) CommitWorktree(_ context.Context, _ int64, worktreeName, _ string) error {
	f.commits = append(f.commits, worktreeName)
	return nil
}

func (f *fakeMerger) MergeWorktree(_ context.Context, _ int64, worktreeName, targetBranch string) error {
	if f.mergeFail != nil {
		if err, ok := f.mergeFail[worktreeName]; ok {
			return err
		}
	}
	f.merges = append(f.merges, worktreeName+"->"+targetBranch)
	return nil
}

func (f *fakeMerger) SyncBranchIntoWorktree(_ context.Context, _ int64, worktreeName, sourceBranch string) error {
	f.syncs = append(f.syncs, worktreeName+"<-"+sourceBranch)
	return nil
}

func (e *epicTestEnv) makeWorktree(t *testing.T, workspaceID int64, name string) {
	t.Helper()
	_, err := e.q.CreateWorktree(e.ctx, store.CreateWorktreeParams{
		WorkspaceID:  workspaceID,
		RepositoryID: 1,
		WorktreePath: "/ws/.worktrees/" + name,
		Branch:       "feature",
		BaseBranch:   "epic/1",
	})
	require.NoError(t, err)
}

// When the review/control workspace's agent finishes, the epic ends at 'done'.
func TestEpicExecution_ReviewWorkspaceDoneMarksEpicDone(t *testing.T) {
	e := setupEpicTest(t)
	colID := e.makeProjectColumn(t)
	epicID := e.makeEpic(t, colID, "Epic ReviewDone")
	_ = e.makeChild(t, colID, epicID, 0, "c0")
	// Put the epic into the review phase directly.
	require.NoError(t, e.q.SetIssueExecStatus(e.ctx, store.SetIssueExecStatusParams{ExecStatus: "reviewing", ID: epicID}))

	// Completer mirrors production: marking the review workspace done drives the
	// completion event for the epic issue.
	comp := &fakeCompleter{complete: func(_ int64) {
		e.svc.OnWorkspaceCompleted(e.ctx, epicID, true)
	}}
	e.svc.SetCompleter(comp)

	wsID := e.makeWorkspace(t, epicID)
	e.svc.OnAgentDone(e.ctx, wsID)
	assert.Equal(t, []int64{wsID}, comp.done, "review workspace auto-marked done")
	assert.Equal(t, "done", e.execStatus(t, epicID), "review complete -> epic done")
}

// --- §22.7/§23.1 epic review human confirmation -----------------------------

// fakeReviewConfirmer records each epic it is asked to confirm and returns a
// configurable verdict. asked (optional) is signalled after every ask so a test can
// synchronise with the async confirm goroutine.
type fakeReviewConfirmer struct {
	mu      sync.Mutex
	calls   []int64
	confirm bool
	asked   chan struct{}
}

func (f *fakeReviewConfirmer) ConfirmEpicReview(_ context.Context, _ store.Workspace, epic store.Issue, _ []store.Issue) (bool, error) {
	f.mu.Lock()
	f.calls = append(f.calls, epic.ID)
	f.mu.Unlock()
	if f.asked != nil {
		f.asked <- struct{}{}
	}
	return f.confirm, nil
}

func (f *fakeReviewConfirmer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// Epic review: user confirms -> the epic finalizes through the completer.
func TestEpicExecution_OnAgentDone_EpicConfirmYes_Completes(t *testing.T) {
	e := setupEpicTest(t)
	colID := e.makeProjectColumn(t)
	epicID := e.makeEpic(t, colID, "Epic ConfirmYes")
	_ = e.makeChild(t, colID, epicID, 0, "c0")
	require.NoError(t, e.q.SetIssueExecStatus(e.ctx, store.SetIssueExecStatusParams{ExecStatus: "reviewing", ID: epicID}))

	doneCh := make(chan int64, 1)
	comp := &fakeCompleter{complete: func(wsID int64) {
		e.svc.OnWorkspaceCompleted(e.ctx, epicID, true)
		doneCh <- wsID
	}}
	e.svc.SetCompleter(comp)
	conf := &fakeReviewConfirmer{confirm: true}
	e.svc.SetReviewConfirmer(conf)

	wsID := e.makeWorkspace(t, epicID)
	e.svc.OnAgentDone(e.ctx, wsID)

	select {
	case got := <-doneCh:
		assert.Equal(t, wsID, got)
	case <-time.After(2 * time.Second):
		t.Fatal("epic was not completed after a confirm")
	}
	// Channel receive establishes happens-before: comp.done is already written.
	assert.Equal(t, []int64{wsID}, comp.done, "confirmed epic auto-marked done")
	assert.Equal(t, 1, conf.count(), "user asked exactly once")
	assert.Equal(t, "done", e.execStatus(t, epicID))
}

// Epic review: user declines -> the epic stays in review, never finalized.
func TestEpicExecution_OnAgentDone_EpicConfirmNo_StaysInReview(t *testing.T) {
	e := setupEpicTest(t)
	colID := e.makeProjectColumn(t)
	epicID := e.makeEpic(t, colID, "Epic ConfirmNo")
	_ = e.makeChild(t, colID, epicID, 0, "c0")
	require.NoError(t, e.q.SetIssueExecStatus(e.ctx, store.SetIssueExecStatusParams{ExecStatus: "reviewing", ID: epicID}))

	comp := &fakeCompleter{}
	e.svc.SetCompleter(comp)
	asked := make(chan struct{}, 1)
	conf := &fakeReviewConfirmer{confirm: false, asked: asked}
	e.svc.SetReviewConfirmer(conf)

	wsID := e.makeWorkspace(t, epicID)
	e.svc.OnAgentDone(e.ctx, wsID)

	select {
	case <-asked:
	case <-time.After(2 * time.Second):
		t.Fatal("user was never asked to confirm")
	}
	// Give the (no-op) confirm goroutine a moment to return, then assert no completion.
	time.Sleep(50 * time.Millisecond)
	assert.Empty(t, comp.done, "declined epic must NOT be finalized")
	assert.Equal(t, "reviewing", e.execStatus(t, epicID), "declined epic stays in review")
}

// With a confirmer wired, a CHILD still auto-completes silently (no confirm) —
// only the epic itself is gated on human confirmation (§22.7).
func TestEpicExecution_OnAgentDone_ChildSilentDespiteConfirmer(t *testing.T) {
	e := setupEpicTest(t)
	colID := e.makeProjectColumn(t)
	epicID := e.makeEpic(t, colID, "Epic ChildSilent")
	c0 := e.makeChild(t, colID, epicID, 0, "c0")

	comp := &fakeCompleter{complete: func(_ int64) {
		_, _ = e.q.UpdateIssueLifecycle(e.ctx, store.UpdateIssueLifecycleParams{ID: c0, LifecycleStatus: "completed"})
		e.svc.OnWorkspaceCompleted(e.ctx, c0, true)
	}}
	e.svc.SetCompleter(comp)
	conf := &fakeReviewConfirmer{confirm: true}
	e.svc.SetReviewConfirmer(conf)
	// Orchestration agent marked the epic running when it created the orchestration ws.
	require.NoError(t, e.q.SetIssueExecStatus(e.ctx, store.SetIssueExecStatusParams{ExecStatus: "running", ID: epicID}))

	wsID := e.makeWorkspace(t, c0)
	e.svc.OnAgentDone(e.ctx, wsID) // child path is synchronous

	assert.Equal(t, []int64{wsID}, comp.done, "child auto-completed silently")
	assert.Equal(t, 0, conf.count(), "child is never put to the user for confirmation")
}

// --- Mode-B (orchestration-agent) capability-tool coverage ---------------------
//
// The mode-A wave engine (ExecuteEpic/startWave/advance + the in-process run map)
// was retired in stage 9 (spec §8/§13). These tests exercise the retained capability
// tools on the pure orchestration path — an epic driven by its orchestration agent,
// never passed to any wave scheduler — covering the three responsibilities the
// deletion gate required: 合并串行化 / 失败策略 / 事件收口.

// 合并串行化 + 子任务收口, mode-B: a child of an epic that was never wave-executed
// auto-completes via OnAgentDone -> its worktree is committed + merged into the epic
// feature branch (mergeChildIntoEpic) and then finalized (RequestWorkspaceCompletion),
// with NO ExecuteEpic / wave engine involved.
func TestEpicExecution_ModeB_ChildMergeWithoutWaveEngine(t *testing.T) {
	e := setupEpicTest(t)
	colID := e.makeProjectColumn(t)
	epicID := e.makeEpic(t, colID, "Epic ModeB Merge")
	c0 := e.makeChild(t, colID, epicID, 0, "c0")
	// Orchestration agent marked the epic running when it created the orchestration ws.
	require.NoError(t, e.q.SetIssueExecStatus(e.ctx, store.SetIssueExecStatusParams{ExecStatus: "running", ID: epicID}))

	merger := &fakeMerger{}
	e.svc.SetMerger(merger)
	comp := &fakeCompleter{complete: func(wsID int64) {
		_, _ = e.q.UpdateIssueLifecycle(e.ctx, store.UpdateIssueLifecycleParams{ID: c0, LifecycleStatus: "completed"})
	}}
	e.svc.SetCompleter(comp)

	wsID := e.makeWorkspace(t, c0)
	e.makeWorktree(t, wsID, "repo-a")

	// No ExecuteEpic: the epic is NOT in the wave-engine running map.
	e.svc.OnAgentDone(e.ctx, wsID)

	assert.Equal(t, []string{"repo-a"}, merger.commits, "child worktree committed via capability tool")
	assert.Equal(t, []string{"repo-a->" + epicBranch(epicID)}, merger.merges, "merged into epic branch without wave engine")
	assert.Equal(t, []int64{wsID}, comp.done, "child finalized through the completion choke point")
}

// epic 执行分支使用优化: after a child merges into the epic feature branch, that branch
// is synced back INTO the epic's own workspace so it reflects the accumulated child
// work (while its diff still bases on the real baseline). Asserts SyncBranchIntoWorktree
// is invoked on the epic workspace's worktree with the epic feature branch as source.
func TestEpicExecution_ChildMergeSyncsEpicBranchIntoEpicWorkspace(t *testing.T) {
	e := setupEpicTest(t)
	colID := e.makeProjectColumn(t)
	epicID := e.makeEpic(t, colID, "Epic SyncBack")
	c0 := e.makeChild(t, colID, epicID, 0, "c0")
	require.NoError(t, e.q.SetIssueExecStatus(e.ctx, store.SetIssueExecStatusParams{ExecStatus: "running", ID: epicID}))

	merger := &fakeMerger{}
	e.svc.SetMerger(merger)
	comp := &fakeCompleter{complete: func(_ int64) {
		_, _ = e.q.UpdateIssueLifecycle(e.ctx, store.UpdateIssueLifecycleParams{ID: c0, LifecycleStatus: "completed"})
	}}
	e.svc.SetCompleter(comp)

	// The epic's own active workspace (orchestration/review) with one worktree —
	// the merge target the epic branch is synced back into.
	epicWsID := e.makeWorkspace(t, epicID)
	e.makeWorktree(t, epicWsID, "epic-repo")

	// The completing child's workspace + worktree.
	childWsID := e.makeWorkspace(t, c0)
	e.makeWorktree(t, childWsID, "repo-a")

	e.svc.OnAgentDone(e.ctx, childWsID)

	assert.Equal(t, []string{"repo-a->" + epicBranch(epicID)}, merger.merges, "child merged into epic branch")
	assert.Equal(t, []string{"epic-repo<-" + epicBranch(epicID)}, merger.syncs,
		"epic branch synced back into the epic workspace's worktree")
}

// 失败策略 fallback, mode-B: when a child merge conflicts and the epic is NOT tracked
// in-memory (pure orchestration), failChildIntegration marks the child 'failed' so its
// state is not silently lost, but it does NOT unilaterally fail the epic — failure
// strategy on this path belongs to the orchestration agent (it observes the failed
// child and decides). Exercises the not-tracked branch of failChildIntegration.
func TestEpicExecution_ModeB_ChildMergeConflictMarksChildFailedNotEpic(t *testing.T) {
	e := setupEpicTest(t)
	colID := e.makeProjectColumn(t)
	epicID := e.makeEpic(t, colID, "Epic ModeB Conflict")
	c0 := e.makeChild(t, colID, epicID, 0, "c0")
	require.NoError(t, e.q.SetIssueExecStatus(e.ctx, store.SetIssueExecStatusParams{ExecStatus: "running", ID: epicID}))

	merger := &fakeMerger{mergeFail: map[string]error{"repo-a": errMergeConflict}}
	e.svc.SetMerger(merger)
	comp := &fakeCompleter{}
	e.svc.SetCompleter(comp)

	wsID := e.makeWorkspace(t, c0)
	e.makeWorktree(t, wsID, "repo-a")

	e.svc.OnAgentDone(e.ctx, wsID)

	assert.Empty(t, comp.done, "child not finalized on merge conflict")
	assert.Equal(t, "failed", e.execStatus(t, c0), "child marked failed via capability tool")
	assert.Equal(t, "running", e.execStatus(t, epicID), "epic untouched: failure strategy is the orchestration agent's call")
}

// 多仓合并语义 (§23.3), mode-B: a partial multi-repo merge lands the child in the
// blocked-needs-human terminal state with a reason naming the integrated vs failed
// repos, on the pure orchestration path (epic not tracked) — same non-atomic semantics
// as the wave-engine path, without a wave-engine failure policy being applied to the epic.
func TestEpicExecution_ModeB_PartialMultiRepoMergeBlocksWithoutWaveEngine(t *testing.T) {
	e := setupEpicTest(t)
	colID := e.makeProjectColumn(t)
	epicID := e.makeEpic(t, colID, "Epic ModeB MultiRepo")
	c0 := e.makeChild(t, colID, epicID, 0, "c0")
	require.NoError(t, e.q.SetIssueExecStatus(e.ctx, store.SetIssueExecStatusParams{ExecStatus: "running", ID: epicID}))

	merger := &fakeMerger{mergeFail: map[string]error{"repo-b": errMergeConflict}}
	e.svc.SetMerger(merger)
	e.svc.SetExecEventService(service.NewExecEventService(e.db))
	comp := &fakeCompleter{}
	e.svc.SetCompleter(comp)

	wsID := e.makeWorkspace(t, c0)
	for i, name := range []string{"repo-a", "repo-b"} { // distinct repository_id per worktree
		_, err := e.q.CreateWorktree(e.ctx, store.CreateWorktreeParams{
			WorkspaceID: wsID, RepositoryID: int64(i + 1),
			WorktreePath: "/ws/.worktrees/" + name, Branch: "feature", BaseBranch: epicBranch(epicID),
		})
		require.NoError(t, err)
	}

	e.svc.OnAgentDone(e.ctx, wsID)

	assert.Empty(t, comp.done, "child not finalized on partial multi-repo merge")
	assert.Equal(t, "gate_blocked", e.execStatus(t, c0), "partial merge lands blocked-needs-human without the wave engine")
	iss, err := e.q.GetIssue(e.ctx, c0)
	require.NoError(t, err)
	require.True(t, iss.ExecStatusReason.Valid)
	assert.Contains(t, iss.ExecStatusReason.String, "repo-a")
	assert.Contains(t, iss.ExecStatusReason.String, "repo-b")
	assert.Equal(t, "running", e.execStatus(t, epicID), "epic untouched: no wave-engine failure policy on the orchestration path")
}

// merge-to-main requires the epic to be 'done' and sends a merge prompt to the
// epic's active control workspace agent.
func TestEpicExecution_RequestMergeToMain(t *testing.T) {
	e := setupEpicTest(t)
	proxy := &fakeAgentProxy{}
	e.svc.SetAgentProxy(proxy)
	colID := e.makeProjectColumn(t)
	epicID := e.makeEpic(t, colID, "Epic MTM")
	require.NoError(t, e.q.SetIssueExecStatus(e.ctx, store.SetIssueExecStatusParams{ExecStatus: "done", ID: epicID}))

	// Active (non-archived) control workspace bound to the epic.
	_ = e.makeWorkspace(t, epicID)

	require.NoError(t, e.svc.RequestMergeToMain(e.ctx, epicID))
	require.NotEmpty(t, proxy.kickoffs)
	last := proxy.kickoffs[len(proxy.kickoffs)-1]
	assert.Contains(t, last, "合并 Epic")
	assert.Contains(t, last, epicBranch(epicID))
}

func TestEpicExecution_RequestMergeToMainNoWorkspace(t *testing.T) {
	e := setupEpicTest(t)
	proxy := &fakeAgentProxy{}
	e.svc.SetAgentProxy(proxy)
	colID := e.makeProjectColumn(t)
	epicID := e.makeEpic(t, colID, "Epic MTM None")
	require.NoError(t, e.q.SetIssueExecStatus(e.ctx, store.SetIssueExecStatusParams{ExecStatus: "done", ID: epicID}))

	err := e.svc.RequestMergeToMain(e.ctx, epicID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no active control workspace")
}
