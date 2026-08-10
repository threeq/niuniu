package service_test

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// ckptTestEnv wires an in-memory store + a real single-repo git worktree whose
// path is used as the workspace path (the CheckpointService's fallback target when
// no worktrees/repositories rows exist), so the whole snapshot/timeline/revert flow
// runs end-to-end against real git.
type ckptTestEnv struct {
	svc     *service.CheckpointService
	q       *store.Queries
	ctx     context.Context
	repoDir string
	issueID int64
	wsID    int64
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func writeF(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
}

func setupCheckpointTest(t *testing.T) *ckptTestEnv {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(store.Schema)
	require.NoError(t, err)
	store.Migrate(db)
	q := store.New(db)
	ctx := context.Background()

	activitySvc := service.NewIssueActivityService(q)
	kanban := service.NewKanbanService(db, q, activitySvc, nil, nil)

	proj, err := q.CreateProject(ctx, store.CreateProjectParams{Name: "p", OwnerType: "user", OwnerID: 1})
	require.NoError(t, err)
	col, err := q.CreateColumn(ctx, store.CreateColumnParams{ProjectID: proj.ID, Name: "实现", Position: 0})
	require.NoError(t, err)
	issue, err := kanban.CreateIssue(ctx, col.ID, "checkpoint test", "", 0, 0, "", "", "", 0, nil, "task", 0, nil, nil, 0)
	require.NoError(t, err)

	// Real git repo used as the workspace path.
	repoDir := t.TempDir()
	gitRun(t, "", "init", "-q", "-b", "main", repoDir)
	gitRun(t, repoDir, "config", "user.email", "test@example.com")
	gitRun(t, repoDir, "config", "user.name", "test")
	gitRun(t, repoDir, "config", "commit.gpgsign", "false")
	gitRun(t, repoDir, "config", "core.autocrlf", "false")
	writeF(t, filepath.Join(repoDir, "code.txt"), "base\n")
	gitRun(t, repoDir, "add", "-A")
	gitRun(t, repoDir, "commit", "-q", "-m", "init")

	ws, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
		IssueID:   sql.NullInt64{Int64: issue.ID, Valid: true},
		Name:      "ws",
		Path:      repoDir,
		Status:    "running",
		OwnerType: "user",
		OwnerID:   1,
	})
	require.NoError(t, err)

	svc := service.NewCheckpointService(store.Wrap(db), q)
	return &ckptTestEnv{svc: svc, q: q, ctx: ctx, repoDir: repoDir, issueID: issue.ID, wsID: ws.ID}
}

// TestCheckpointService_TimelineDiffRevert exercises the full autohost 安全网 flow:
// advance baseline snapshot -> gate_pass snapshot -> a bad change -> timeline lists
// both steps -> per-step diff -> revert-to-last-passing rewinds the bad change ->
// revert to step 1 rewinds further, all without losing later checkpoints.
func TestCheckpointService_TimelineDiffRevert(t *testing.T) {
	e := setupCheckpointTest(t)
	codePath := filepath.Join(e.repoDir, "code.txt")

	// Step 1: entering implement column — baseline snapshot of "v1".
	writeF(t, codePath, "v1\n")
	r1, err := e.svc.Snapshot(e.ctx, e.issueID, e.wsID, service.CheckpointKindAdvance, "baseline", "")
	require.NoError(t, err)
	require.Equal(t, 1, r1.Step)
	require.Len(t, r1.Repos, 1)

	// Step 2: gate passes on "v2" — record a gate-passing checkpoint.
	writeF(t, codePath, "v2\n")
	writeF(t, filepath.Join(e.repoDir, "added.txt"), "added-at-2\n")
	r2, err := e.svc.Snapshot(e.ctx, e.issueID, e.wsID, service.CheckpointKindGatePass, "gate ok", service.CheckpointGateStatusPass)
	require.NoError(t, err)
	require.Equal(t, 2, r2.Step)

	// A subsequent BAD change (uncommitted, no checkpoint).
	writeF(t, codePath, "v3-broken\n")

	// Timeline lists both steps, ascending.
	tl, err := e.svc.Timeline(e.ctx, e.issueID)
	require.NoError(t, err)
	require.Len(t, tl, 2)
	require.Equal(t, 1, tl[0].Step)
	require.Equal(t, service.CheckpointKindAdvance, tl[0].Kind)
	require.Equal(t, 2, tl[1].Step)
	require.Equal(t, service.CheckpointGateStatusPass, tl[1].GateStatus)
	// Step 2 chains onto step 1 (so per-step diffs are true deltas, not
	// cumulative-since-HEAD), even on the workspace-path (repository_id NULL) fallback.
	require.Equal(t, tl[0].Repos[0].CommitHash, tl[1].Repos[0].ParentHash,
		"step 2 should parent onto step 1's commit")

	// Per-step diff: step 2 introduced code.txt v1->v2 and added.txt.
	step2Repo := tl[1].Repos[0]
	diffs, err := e.svc.StepDiff(e.ctx, step2Repo.ID)
	require.NoError(t, err)
	paths := map[string]bool{}
	for _, d := range diffs {
		paths[d.Path] = true
	}
	require.True(t, paths["code.txt"], "expected code.txt in step2 diff")
	require.True(t, paths["added.txt"], "expected added.txt in step2 diff")

	// LastPassingStep is step 2.
	step, ok := e.svc.LastPassingStep(e.ctx, e.issueID)
	require.True(t, ok)
	require.Equal(t, 2, step)

	// Revert to last passing wipes the bad v3 change, restoring v2 + added.txt.
	revStep, ok, err := e.svc.RevertToLastPassing(e.ctx, e.issueID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 2, revStep)
	require.Equal(t, "v2\n", readContent(t, codePath))
	require.Equal(t, "added-at-2\n", readContent(t, filepath.Join(e.repoDir, "added.txt")))

	// Revert to step 1: code.txt back to v1, added.txt removed (it did not exist yet).
	_, err = e.svc.Revert(e.ctx, e.issueID, 1)
	require.NoError(t, err)
	require.Equal(t, "v1\n", readContent(t, codePath))
	_, statErr := os.Stat(filepath.Join(e.repoDir, "added.txt"))
	require.True(t, os.IsNotExist(statErr), "added.txt should be gone after revert to step1")

	// Later checkpoints survive: reverting forward to step 2 still works.
	_, err = e.svc.Revert(e.ctx, e.issueID, 2)
	require.NoError(t, err)
	require.Equal(t, "v2\n", readContent(t, codePath))
}

// TestCheckpointService_NilSafe verifies a nil / db-less service degrades to no-ops
// rather than panicking, matching the "best-effort, never blocks" wiring contract.
func TestCheckpointService_NilSafe(t *testing.T) {
	var s *service.CheckpointService
	res, err := s.Snapshot(context.Background(), 1, 1, service.CheckpointKindAdvance, "x", "")
	require.NoError(t, err)
	require.Equal(t, 0, res.Step)
	_, ok := s.LastPassingStep(context.Background(), 1)
	require.False(t, ok)
}

func readContent(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}
