package service_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// Column-native floor gate + completion收口 tests (stage 4; spec
// 2026-06-05-ai-native-board-execution-design.md §5/§11.3/§22/§23.3).

// floorExecStub is a GateSpecExecutor that records every (specID, path) call and
// can be told to fail specific specs and/or specific repo paths.
type floorExecStub struct {
	mu       sync.Mutex
	calls    []floorCall
	failSpec map[int64]bool
	failPath func(specID int64, path string) bool
}

type floorCall struct {
	specID int64
	path   string
}

func (f *floorExecStub) ExecuteSpec(_ context.Context, _, specID int64, path string) (bool, string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, floorCall{specID: specID, path: path})
	f.mu.Unlock()
	if f.failPath != nil && f.failPath(specID, path) {
		return false, "path boom", nil
	}
	if f.failSpec[specID] {
		return false, "spec boom", nil
	}
	return true, "ok", nil
}

func (f *floorExecStub) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *floorExecStub) pathsFor(specID int64) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if c.specID == specID {
			out = append(out, c.path)
		}
	}
	return out
}

// floorKickerStub records KickFloorRetry calls (the autohost self-fix re-engage).
type floorKickerStub struct {
	mu    sync.Mutex
	kicks []int64
}

func (k *floorKickerStub) KickFloorRetry(_ context.Context, workspaceID int64, _ []service.GateFailure) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.kicks = append(k.kicks, workspaceID)
}

func (k *floorKickerStub) count() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.kicks)
}

// floorGateFixture bundles a WorkspaceOpsService wired with a floor gate plus its
// seeded project/column/issue/workspace and the test doubles.
type floorGateFixture struct {
	ops     *service.WorkspaceOpsService
	db      *sql.DB
	q       *store.Queries
	wsID    int64
	issueID int64
	colID   int64
	wsPath  string
	exec    *floorExecStub
	kicker  *floorKickerStub
}

func (fx *floorGateFixture) execStatus(t *testing.T) string {
	t.Helper()
	var s string
	require.NoError(t, fx.db.QueryRow(`SELECT exec_status FROM issues WHERE id = ?`, fx.issueID).Scan(&s))
	return s
}

func (fx *floorGateFixture) wsStatus(t *testing.T) string {
	t.Helper()
	ws, err := fx.q.GetWorkspace(context.Background(), fx.wsID)
	require.NoError(t, err)
	return ws.Status
}

func (fx *floorGateFixture) floorRetry(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, fx.db.QueryRow(`SELECT floor_retry_count FROM issues WHERE id = ?`, fx.issueID).Scan(&n))
	return n
}

// seedFloorSpec inserts a global harness_spec with the given severity and binds it
// to the column with applicability='always' (a floor / 底线 spec).
func (fx *floorGateFixture) seedFloorSpec(t *testing.T, idx int, severity string) int64 {
	t.Helper()
	res, err := fx.db.Exec(
		`INSERT INTO harness_specs (category, name, enabled, severity, config)
		 VALUES ('quality',?,1,?, '')`,
		fmt.Sprintf("floor-spec-%d-%s", idx, severity), severity)
	require.NoError(t, err)
	specID, err := res.LastInsertId()
	require.NoError(t, err)
	_, err = fx.db.Exec(
		`INSERT INTO column_gate_specs (column_id, spec_id, position, applicability) VALUES (?, ?, ?, 'always')`,
		fx.colID, specID, idx)
	require.NoError(t, err)
	return specID
}

// setupFloorGate builds a fixture with the workspace pre-seeded at wsStatus and the
// floor gate wired with the given retry limit. No floor specs are bound yet (the
// test seeds them via fx.seedFloorSpec before calling RequestWorkspaceCompletion).
func setupFloorGate(t *testing.T, wsStatus string, retryLimit int) *floorGateFixture {
	t.Helper()
	ctx := context.Background()
	db, q := setupMarkDoneDB(t)

	proj, err := q.CreateProject(ctx, store.CreateProjectParams{Name: "fp-" + wsStatus, OwnerType: "user"})
	require.NoError(t, err)
	col, err := q.CreateColumn(ctx, store.CreateColumnParams{ProjectID: proj.ID, Name: "done", Position: 0})
	require.NoError(t, err)
	issue, err := q.CreateIssue(ctx, store.CreateIssueParams{ColumnID: col.ID, Title: "floor issue", Position: 0})
	require.NoError(t, err)
	wsPath := t.TempDir()
	ws, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
		IssueID:   sql.NullInt64{Int64: issue.ID, Valid: true},
		Name:      "fws-" + wsStatus,
		Path:      wsPath,
		Status:    wsStatus,
		OwnerType: "user",
		OwnerID:   0,
	})
	require.NoError(t, err)

	wsSvc := service.NewWorkspaceService(q, db, nil, t.TempDir(), nil, nil)
	activitySvc := service.NewIssueActivityService(q)
	kbSvc := service.NewKanbanService(db, q, activitySvc, nil, nil)
	ops := service.NewWorkspaceOpsService(q, wsSvc, kbSvc, nil)

	exec := &floorExecStub{failSpec: map[int64]bool{}}
	kicker := &floorKickerStub{}
	ops.SetFloorGateDeps(db, exec, kicker, retryLimit)

	return &floorGateFixture{
		ops: ops, db: db, q: q, wsID: ws.ID, issueID: issue.ID, colID: col.ID,
		wsPath: wsPath, exec: exec, kicker: kicker,
	}
}

// waitFor polls cond until true or the deadline, failing the test on timeout.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", msg)
}

// --- No floor gate: finalize synchronously --------------------------------

func TestRequestCompletion_NoFloorSpec_FinalizesInline(t *testing.T) {
	fx := setupFloorGate(t, "needs_review", 0)
	res, err := fx.ops.RequestWorkspaceCompletion(context.Background(), fx.wsID, service.TriggerHuman)
	require.NoError(t, err)
	require.Equal(t, service.CompletionFinalized, res.Status)
	require.Equal(t, "completed", fx.wsStatus(t))
	require.Equal(t, 0, fx.exec.callCount(), "no specs => executor never called")
}

func TestRequestCompletion_RejectsRunning(t *testing.T) {
	fx := setupFloorGate(t, "running", 0)
	_, err := fx.ops.RequestWorkspaceCompletion(context.Background(), fx.wsID, service.TriggerHuman)
	require.ErrorIs(t, err, service.ErrWorkspaceRunning)
	require.Equal(t, "running", fx.wsStatus(t))
}

func TestRequestCompletion_IdempotentOnCompleted(t *testing.T) {
	fx := setupFloorGate(t, "completed", 0)
	fx.seedFloorSpec(t, 0, "error") // even with a floor spec, completed short-circuits
	res, err := fx.ops.RequestWorkspaceCompletion(context.Background(), fx.wsID, service.TriggerHuman)
	require.NoError(t, err)
	require.Equal(t, service.CompletionFinalized, res.Status)
	require.Equal(t, 0, fx.exec.callCount())
}

// --- Floor gate passes: finalize ------------------------------------------

func TestRequestCompletion_FloorGatePasses_Finalizes(t *testing.T) {
	fx := setupFloorGate(t, "needs_review", 0)
	fx.seedFloorSpec(t, 0, "error")
	res, err := fx.ops.RequestWorkspaceCompletion(context.Background(), fx.wsID, service.TriggerHuman)
	require.NoError(t, err)
	require.Equal(t, service.CompletionGateChecking, res.Status)

	waitFor(t, func() bool { return fx.wsStatus(t) == "completed" }, "workspace completed after passing gate")
	require.Equal(t, "done", fx.execStatus(t))
}

// --- Floor gate blocks (human trigger): not completed, exec_status blocked --

func TestRequestCompletion_FloorGateBlocks_Human_NoComplete(t *testing.T) {
	fx := setupFloorGate(t, "needs_review", 0)
	specID := fx.seedFloorSpec(t, 0, "error")
	fx.exec.failSpec[specID] = true

	res, err := fx.ops.RequestWorkspaceCompletion(context.Background(), fx.wsID, service.TriggerHuman)
	require.NoError(t, err)
	require.Equal(t, service.CompletionGateChecking, res.Status)

	waitFor(t, func() bool { return fx.execStatus(t) == "gate_blocked" }, "issue exec_status gate_blocked")
	// Human path: workspace is NOT completed and NOT escalated — left for the user.
	require.Equal(t, "needs_review", fx.wsStatus(t))
	require.NotEqual(t, "completed", fx.wsStatus(t))
	require.Equal(t, 0, fx.kicker.count(), "human trigger never re-kicks the agent")
}

// --- Floor gate blocks (auto trigger): re-kick under limit -----------------

func TestRequestCompletion_FloorGateBlocks_Auto_KicksUnderLimit(t *testing.T) {
	fx := setupFloorGate(t, "needs_review", 3)
	specID := fx.seedFloorSpec(t, 0, "error")
	fx.exec.failSpec[specID] = true

	_, err := fx.ops.RequestWorkspaceCompletion(context.Background(), fx.wsID, service.TriggerAuto)
	require.NoError(t, err)

	waitFor(t, func() bool { return fx.kicker.count() == 1 }, "auto self-fix kick under limit")
	require.Equal(t, "gate_blocked", fx.execStatus(t))
	require.Equal(t, 1, fx.floorRetry(t), "floor_retry_count incremented")
	require.NotEqual(t, "completed", fx.wsStatus(t))
}

// --- Floor gate blocks (auto trigger): escalate over limit -----------------

func TestRequestCompletion_FloorGateBlocks_Auto_EscalatesOverLimit(t *testing.T) {
	fx := setupFloorGate(t, "needs_review", 2)
	specID := fx.seedFloorSpec(t, 0, "error")
	fx.exec.failSpec[specID] = true
	// Pre-set floor_retry_count to the limit so the next failure exceeds it.
	_, err := fx.db.Exec(`UPDATE issues SET floor_retry_count = 2 WHERE id = ?`, fx.issueID)
	require.NoError(t, err)

	_, err = fx.ops.RequestWorkspaceCompletion(context.Background(), fx.wsID, service.TriggerAuto)
	require.NoError(t, err)

	waitFor(t, func() bool { return fx.wsStatus(t) == "attention" }, "escalated to attention over retry limit")
	require.Equal(t, "gate_blocked", fx.execStatus(t))
	require.Equal(t, 0, fx.kicker.count(), "over limit does not kick, it escalates")
}

// --- Advisory (warning) severity failure does not block --------------------

func TestRequestCompletion_WarningSeverityFailure_StillFinalizes(t *testing.T) {
	fx := setupFloorGate(t, "needs_review", 0)
	warnSpec := fx.seedFloorSpec(t, 0, "warning")
	fx.exec.failSpec[warnSpec] = true // fails, but warning is advisory (放行)

	_, err := fx.ops.RequestWorkspaceCompletion(context.Background(), fx.wsID, service.TriggerHuman)
	require.NoError(t, err)
	waitFor(t, func() bool { return fx.wsStatus(t) == "completed" }, "warning failure does not block completion")
	require.Equal(t, "done", fx.execStatus(t))
}

// --- Multi-repo aggregation (§23.3): any repo error = overall block --------

func TestRequestCompletion_MultiRepo_AnyRepoErrorBlocks(t *testing.T) {
	fx := setupFloorGate(t, "needs_review", 3)
	// Two repo worktrees on disk so worktreePaths() yields two paths.
	worktrees := filepath.Join(fx.wsPath, ".worktrees")
	require.NoError(t, os.MkdirAll(filepath.Join(worktrees, "repoA"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(worktrees, "repoB"), 0o755))

	specID := fx.seedFloorSpec(t, 0, "error")
	// Spec passes in repoA but fails in repoB -> overall block.
	fx.exec.failPath = func(_ int64, path string) bool {
		return filepath.Base(path) == "repoB"
	}

	_, err := fx.ops.RequestWorkspaceCompletion(context.Background(), fx.wsID, service.TriggerAuto)
	require.NoError(t, err)

	waitFor(t, func() bool { return fx.execStatus(t) == "gate_blocked" }, "any-repo error blocks")
	require.NotEqual(t, "completed", fx.wsStatus(t))
	// Both repos were attempted for the spec (repoA passed, repoB failed).
	paths := fx.exec.pathsFor(specID)
	require.GreaterOrEqual(t, len(paths), 1, "spec ran against repo worktrees, got %v", paths)
}

// --- Multi-repo: all repos pass -> finalize --------------------------------

func TestRequestCompletion_MultiRepo_AllPass_Finalizes(t *testing.T) {
	fx := setupFloorGate(t, "needs_review", 0)
	worktrees := filepath.Join(fx.wsPath, ".worktrees")
	require.NoError(t, os.MkdirAll(filepath.Join(worktrees, "repoA"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(worktrees, "repoB"), 0o755))
	specID := fx.seedFloorSpec(t, 0, "error")

	_, err := fx.ops.RequestWorkspaceCompletion(context.Background(), fx.wsID, service.TriggerHuman)
	require.NoError(t, err)

	waitFor(t, func() bool { return fx.wsStatus(t) == "completed" }, "all repos pass -> completed")
	require.ElementsMatch(t, []string{
		filepath.Join(worktrees, "repoA"),
		filepath.Join(worktrees, "repoB"),
	}, fx.exec.pathsFor(specID), "spec ran once per repo worktree")
}

// --- Single-completion invariant (§22.5) ----------------------------------
// A blocked floor gate must never flip the workspace to 'completed'. Combined with
// the fact that finalizeCompletion is the only UpdateWorkspaceStatus('completed')
// path (MarkWorkspaceDone + CompleteWorkspace both route through it), this asserts
// the底线 cannot be bypassed by the completion entry point.

func TestRequestCompletion_BlockedNeverCompletes_Invariant(t *testing.T) {
	fx := setupFloorGate(t, "needs_review", 0)
	specID := fx.seedFloorSpec(t, 0, "error")
	fx.exec.failSpec[specID] = true

	_, err := fx.ops.RequestWorkspaceCompletion(context.Background(), fx.wsID, service.TriggerHuman)
	require.NoError(t, err)
	waitFor(t, func() bool { return fx.execStatus(t) == "gate_blocked" }, "gate blocked")

	// Give any erroneous async finalize a chance to run, then assert it never did.
	time.Sleep(100 * time.Millisecond)
	require.NotEqual(t, "completed", fx.wsStatus(t), "blocked floor gate must not complete the workspace")
}

// --- §23.6 output probing: floor gate by issue output type -----------------

// initGitRepoAt makes dir a git repo with one base commit (so HEAD exists) and
// returns a runner for further git commands. Used to control the diff/output state
// the floor gate probes.
func initGitRepoAt(t *testing.T, dir string) func(args ...string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t.io",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t.io",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("init")
	run("config", "commit.gpgsign", "false")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("base\n"), 0o644))
	run("add", "-A")
	run("commit", "-m", "base")
	return run
}

// seedFloorSpecCode is seedFloorSpec + code_probe_only=1 (a build/test-class floor,
// §23.6) so the floor gate auto-N/As it for a no-code-diff issue.
func (fx *floorGateFixture) seedFloorSpecCode(t *testing.T, idx int, severity string) int64 {
	t.Helper()
	specID := fx.seedFloorSpec(t, idx, severity)
	_, err := fx.db.Exec(`UPDATE harness_specs SET code_probe_only = 1 WHERE id = ?`, specID)
	require.NoError(t, err)
	return specID
}

// No code diff but the doc/research issue produced an (untracked) file: the
// code-class floor is N/A (never executed) and 产出非空 is satisfied -> finalize.
func TestRequestCompletion_NoCodeDiff_CodeSpecNA_Finalizes(t *testing.T) {
	fx := setupFloorGate(t, "needs_review", 0)
	initGitRepoAt(t, fx.wsPath)
	require.NoError(t, os.WriteFile(filepath.Join(fx.wsPath, "NOTES.md"), []byte("research\n"), 0o644))
	specID := fx.seedFloorSpecCode(t, 0, "error")
	fx.exec.failSpec[specID] = true // would block IF it ran

	_, err := fx.ops.RequestWorkspaceCompletion(context.Background(), fx.wsID, service.TriggerHuman)
	require.NoError(t, err)
	waitFor(t, func() bool { return fx.wsStatus(t) == "completed" }, "doc issue completes (code floor N/A)")
	require.Equal(t, "done", fx.execStatus(t))
	require.Equal(t, 0, fx.exec.callCount(), "code-class floor must not run for a no-code-diff issue")
}

// No code diff AND nothing produced at all: code floor N/A, but the 产出非空 non-code
// 兜底 blocks so an empty doc workspace cannot silently complete.
func TestRequestCompletion_NoCodeDiff_NoOutput_FallbackBlocks(t *testing.T) {
	fx := setupFloorGate(t, "needs_review", 0)
	initGitRepoAt(t, fx.wsPath) // clean repo, no further changes
	fx.seedFloorSpecCode(t, 0, "error")

	_, err := fx.ops.RequestWorkspaceCompletion(context.Background(), fx.wsID, service.TriggerHuman)
	require.NoError(t, err)
	waitFor(t, func() bool { return fx.execStatus(t) == "gate_blocked" }, "empty workspace blocked by 产出非空")
	require.NotEqual(t, "completed", fx.wsStatus(t))
	require.Equal(t, 0, fx.exec.callCount(), "code floor N/A, never executed")
}

// A real code diff: the code-class floor runs as before (N/A only on no-diff).
func TestRequestCompletion_CodeDiff_CodeSpecRuns(t *testing.T) {
	fx := setupFloorGate(t, "needs_review", 0)
	initGitRepoAt(t, fx.wsPath)
	require.NoError(t, os.WriteFile(filepath.Join(fx.wsPath, "README.md"), []byte("changed\n"), 0o644))
	specID := fx.seedFloorSpecCode(t, 0, "error")
	fx.exec.failSpec[specID] = true

	_, err := fx.ops.RequestWorkspaceCompletion(context.Background(), fx.wsID, service.TriggerHuman)
	require.NoError(t, err)
	waitFor(t, func() bool { return fx.execStatus(t) == "gate_blocked" }, "code floor runs when a diff exists")
	require.GreaterOrEqual(t, fx.exec.callCount(), 1, "code-class floor executed (code diff present)")
}

// A non-code floor spec present provides the门槛 even with no code diff / no output:
// it runs (and the code-class spec is N/A), so the built-in兜底 does not fire.
func TestRequestCompletion_NoCodeDiff_NonCodeFloorRuns(t *testing.T) {
	fx := setupFloorGate(t, "needs_review", 0)
	initGitRepoAt(t, fx.wsPath) // clean: no diff, no output
	codeSpec := fx.seedFloorSpecCode(t, 0, "error") // N/A
	nonCode := fx.seedFloorSpec(t, 1, "error")      // runs + passes
	fx.exec.failSpec[codeSpec] = true               // would block but is N/A

	_, err := fx.ops.RequestWorkspaceCompletion(context.Background(), fx.wsID, service.TriggerHuman)
	require.NoError(t, err)
	waitFor(t, func() bool { return fx.wsStatus(t) == "completed" }, "non-code floor passes -> completes")
	require.Equal(t, []string{fx.wsPath}, fx.exec.pathsFor(nonCode), "non-code floor ran")
	require.Empty(t, fx.exec.pathsFor(codeSpec), "code floor N/A, not run")
}

// RecoverFloorGates收口 a workspace stuck in gate_checking after a crash.
func TestRecoverFloorGates_ResetsStuckGateChecking(t *testing.T) {
	fx := setupFloorGate(t, "needs_review", 0)
	_, err := fx.db.Exec(`UPDATE issues SET exec_status = 'gate_checking' WHERE id = ?`, fx.issueID)
	require.NoError(t, err)

	fx.ops.RecoverFloorGates(context.Background())

	require.Equal(t, "gate_blocked", fx.execStatus(t))
	require.Equal(t, "attention", fx.wsStatus(t))
}
