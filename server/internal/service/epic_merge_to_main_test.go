package service

// Epic 统一分支 Wave 2 (spec docs/superpowers/specs/2026-09-28-epic-unified-branch-design.md
// §4 C：服务端收口 merge-to-main)。
//
// Real-git tests running the full server-side merge flow: ① main→epic
// pre-merge (conflict → structured 409 payload, refs untouched), ② epic→main
// fast-forward, ③ control-workspace sync, then the verification-only prompt.
// Reuses the unifiedBranchEnv helpers from epic_unified_branch_test.go.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
)

// mtmFakeSession / mtmFakeProxy is the package-internal agent proxy fake: it
// records every delivered message so tests can assert the verification prompt.
type mtmFakeSession struct{ kickoffs *[]string }

func (s *mtmFakeSession) SetActiveRunID(int64)         {}
func (s *mtmFakeSession) Cancel(context.Context) error { return nil }
func (s *mtmFakeSession) SendKickoff(_ context.Context, _, msg, _ string) {
	*s.kickoffs = append(*s.kickoffs, msg)
}

type mtmFakeProxy struct{ kickoffs []string }

func (p *mtmFakeProxy) GetOrStartSession(context.Context, int64, int64) (AgentSession, error) {
	return &mtmFakeSession{kickoffs: &p.kickoffs}, nil
}
func (p *mtmFakeProxy) GetSession(int64) AgentSession  { return nil }
func (p *mtmFakeProxy) PrepareUserSend(context.Context, int64) {}

func (p *mtmFakeProxy) Deliver(_ context.Context, _ int64, _, content, _ string) (bool, int64, error) {
	p.kickoffs = append(p.kickoffs, content)
	return false, 0, nil
}

// mtmEnv extends the unified-branch env with what the merge flow needs: a real
// merger (ff-only workspace sync), an exec-event recorder and a fake agent
// proxy capturing the prompts.
func mtmEnv(t *testing.T) (*unifiedBranchEnv, *mtmFakeProxy) {
	t.Helper()
	e := setupUnifiedBranchEnv(t)
	e.epicSvc.SetMerger(NewWorkspaceOpsService(e.q, e.wsSvc, e.kanban, nil))
	e.epicSvc.SetExecEventService(NewExecEventService(e.db))
	proxy := &mtmFakeProxy{}
	e.epicSvc.SetAgentProxy(proxy)
	return e, proxy
}

// mtmControlWorktree returns the control workspace's single worktree path.
func mtmControlWorktree(t *testing.T, e *unifiedBranchEnv, workspaceID int64) string {
	t.Helper()
	wts, err := e.q.ListWorktrees(e.ctx, workspaceID)
	require.NoError(t, err)
	require.Len(t, wts, 1)
	return wts[0].WorktreePath
}

// mtmAddSecondRepo attaches another repo to the env's project and returns its
// on-disk dir.
func mtmAddSecondRepo(t *testing.T, e *unifiedBranchEnv, name string) string {
	t.Helper()
	col, err := e.q.GetColumn(e.ctx, e.columnID)
	require.NoError(t, err)
	dir := filepath.Join(t.TempDir(), name)
	ubRunGit(t, "", "init", "-q", "-b", "main", dir)
	ubRunGit(t, dir, "config", "user.email", "epic@example.com")
	ubRunGit(t, dir, "config", "user.name", "epic-test")
	ubRunGit(t, dir, "config", "commit.gpgsign", "false")
	ubWriteFile(t, filepath.Join(dir, "README.md"), "# "+name+"\n")
	ubRunGit(t, dir, "add", "README.md")
	ubRunGit(t, dir, "commit", "-q", "-m", "init")
	repo, err := e.q.CreateRepository(e.ctx, store.CreateRepositoryParams{
		Name:          name,
		Path:          dir,
		DefaultBranch: sql.NullString{String: "main", Valid: true},
		OwnerType:     "user",
		OwnerID:       1,
	})
	require.NoError(t, err)
	require.NoError(t, e.q.InsertProjectRepository(e.ctx, store.InsertProjectRepositoryParams{
		ProjectID: col.ProjectID, RepositoryID: repo.ID, DefaultBranch: "main",
	}))
	return dir
}

// mtmEventBySummary returns the exec event whose summary contains substr.
func mtmEventBySummary(t *testing.T, e *unifiedBranchEnv, epicID int64, substr string) (store.IssueExecEvent, bool) {
	t.Helper()
	events, err := e.q.ListIssueExecEvents(e.ctx, epicID)
	require.NoError(t, err)
	for _, ev := range events {
		if strings.Contains(ev.Summary, substr) {
			return ev, true
		}
	}
	return store.IssueExecEvent{}, false
}

// TestEpicMergeToMain_ConflictReturnsFilesAndLeavesRefs pins §4 step ①: a
// main→epic conflict stops the flow with the conflicted-file list, both refs
// stay untouched, the control workspace flips to attention, and NO prompt is
// sent to the agent.
func TestEpicMergeToMain_ConflictReturnsFilesAndLeavesRefs(t *testing.T) {
	e, proxy := mtmEnv(t)
	epicID := e.makeEpic(t, "冲突 epic")
	out, err := e.epicSvc.StartWorkspaceForIssue(e.ctx, epicID)
	require.NoError(t, err)
	wtPath := mtmControlWorktree(t, e, out.WorkspaceID)
	epicBranch := fmt.Sprintf("epic/%d", epicID)
	kickoffsBefore := len(proxy.kickoffs) // the OnWorkspaceCreated orchestration kickoff

	// Epic side: rewrite README on the epic branch (via the control worktree).
	ubWriteFile(t, filepath.Join(wtPath, "README.md"), "epic version\n")
	ubRunGit(t, wtPath, "add", "README.md")
	ubRunGit(t, wtPath, "commit", "-q", "-m", "epic side")

	// Main side: rewrite the same file on main — a guaranteed conflict.
	ubWriteFile(t, filepath.Join(e.repoDir, "README.md"), "main version\n")
	ubRunGit(t, e.repoDir, "add", "README.md")
	ubRunGit(t, e.repoDir, "commit", "-q", "-m", "main side")

	mainBefore := ubGitOut(t, e.repoDir, "rev-parse", "main")
	epicBefore := ubGitOut(t, e.repoDir, "rev-parse", epicBranch)

	err = e.epicSvc.RequestMergeToMain(e.ctx, epicID)
	require.Error(t, err)
	var cf *EpicMergeConflictError
	require.True(t, errors.As(err, &cf), "want *EpicMergeConflictError, got %v", err)
	require.NotEmpty(t, cf.ConflictFiles, "the conflict must carry a file list")
	require.Contains(t, cf.ConflictFiles, "README.md")

	// Both refs untouched (MergeAs's merge-tree dry run wrote nothing).
	require.Equal(t, mainBefore, ubGitOut(t, e.repoDir, "rev-parse", "main"), "main ref must not move on conflict")
	require.Equal(t, epicBefore, ubGitOut(t, e.repoDir, "rev-parse", epicBranch), "epic ref must not move on conflict")

	// Control workspace flipped to attention so the epic surfaces for handling.
	ws, err := e.q.GetWorkspace(e.ctx, out.WorkspaceID)
	require.NoError(t, err)
	require.Equal(t, "attention", ws.Status)

	// A "main→epic 冲突待解决" exec event was recorded with the file payload.
	ev, ok := mtmEventBySummary(t, e, epicID, "main→epic 冲突待解决")
	require.True(t, ok, "conflict exec event missing")
	require.Contains(t, ev.DetailJson.String, "README.md")

	// The flow stopped BEFORE engaging the agent: no new prompt was sent.
	require.Equal(t, kickoffsBefore, len(proxy.kickoffs), "no prompt may be sent while the conflict is unresolved")
}

// TestEpicMergeToMain_MainAdvanceMergesAndFastForwards pins the happy path
// with main side progress: main's new commit is pre-merged into the epic
// branch, then main fast-forwards all the way to the epic head, and the
// verification-only prompt (no git merge directives) is sent.
func TestEpicMergeToMain_MainAdvanceMergesAndFastForwards(t *testing.T) {
	e, proxy := mtmEnv(t)
	epicID := e.makeEpic(t, "快进 epic")
	out, err := e.epicSvc.StartWorkspaceForIssue(e.ctx, epicID)
	require.NoError(t, err)
	wtPath := mtmControlWorktree(t, e, out.WorkspaceID)
	epicBranch := fmt.Sprintf("epic/%d", epicID)

	// Epic side: a new file on the epic branch.
	ubWriteFile(t, filepath.Join(wtPath, "epic.txt"), "epic work\n")
	ubRunGit(t, wtPath, "add", "epic.txt")
	ubRunGit(t, wtPath, "commit", "-q", "-m", "epic advance")

	// Main side: an independent commit on a different file — the pre-merge
	// folds it in cleanly before the fast-forward.
	ubWriteFile(t, filepath.Join(e.repoDir, "main.txt"), "main work\n")
	ubRunGit(t, e.repoDir, "add", "main.txt")
	ubRunGit(t, e.repoDir, "commit", "-q", "-m", "main advance")
	mainBefore := ubGitOut(t, e.repoDir, "rev-parse", "main")

	require.NoError(t, e.epicSvc.RequestMergeToMain(e.ctx, epicID))

	// main == epic head: the fast-forward landed exactly on the epic tip.
	epicHead := ubGitOut(t, e.repoDir, "rev-parse", epicBranch)
	require.Equal(t, epicHead, ubGitOut(t, e.repoDir, "rev-parse", "main"),
		"main must fast-forward to the epic head")
	require.NotEqual(t, mainBefore, epicHead, "precondition: main actually moved")

	// The control worktree (checked out ON epic/<id>) picked up the merged
	// content — the ref moved under it and the sync refreshed index+files.
	mainContent, err := os.ReadFile(filepath.Join(wtPath, "main.txt"))
	require.NoError(t, err, "the pre-merged main-side file must be visible in the control worktree")
	require.Equal(t, "main work\n", strings.ReplaceAll(string(mainContent), "\r\n", "\n"))
	require.Equal(t, epicHead, ubGitOut(t, wtPath, "rev-parse", "HEAD"))

	// Both flow nodes wrote exec events.
	_, ok := mtmEventBySummary(t, e, epicID, "main→epic 已同步")
	require.True(t, ok, "missing main→epic sync event")
	_, ok = mtmEventBySummary(t, e, epicID, "epic→main 已合并（快进）")
	require.True(t, ok, "missing epic→main merge event")

	// The prompt is verification-only: no git merge directives for the agent.
	require.NotEmpty(t, proxy.kickoffs)
	last := proxy.kickoffs[len(proxy.kickoffs)-1]
	require.Contains(t, last, "服务端已完成 main→epic 同步与 epic→main 合并（快进）")
	require.Contains(t, last, "验证构建/测试")
	require.Contains(t, last, "推送 origin")
	require.NotContains(t, last, "就地解决", "the agent must not be told to resolve conflicts in place")
	require.NotContains(t, last, "git merge", "the agent must not run any git merge")
}

// TestEpicMergeToMain_MultiRepoPartialConflictEscalates pins §4 多仓: repo A
// merges cleanly, repo B conflicts — repo B's refs stay untouched, the epic is
// escalated to blocked-needs-human (cross-repo merges are non-atomic), the
// control workspace flips to attention, and no prompt is sent.
func TestEpicMergeToMain_MultiRepoPartialConflictEscalates(t *testing.T) {
	e, proxy := mtmEnv(t)
	epicID := e.makeEpic(t, "多仓冲突 epic")
	out, err := e.epicSvc.StartWorkspaceForIssue(e.ctx, epicID)
	require.NoError(t, err)
	wtPath := mtmControlWorktree(t, e, out.WorkspaceID)
	epicBranch := fmt.Sprintf("epic/%d", epicID)
	kickoffsBefore := len(proxy.kickoffs)

	// Attach repo B after the epic workspace exists, then (re-)ensure the epic
	// branch so repo B has it too (what RequestMergeToMain does before merging).
	repoB := mtmAddSecondRepo(t, e, "repo-b")
	require.NoError(t, e.epicSvc.ensureEpicBranch(e.ctx, epicID))

	// Repo A: epic-side work on a new file (clean merge path).
	ubWriteFile(t, filepath.Join(wtPath, "epic.txt"), "epic work\n")
	ubRunGit(t, wtPath, "add", "epic.txt")
	ubRunGit(t, wtPath, "commit", "-q", "-m", "epic advance")

	// Repo B: conflicting edits on both sides of epic/<id>.
	wtB := filepath.Join(t.TempDir(), "repo-b-epic-wt")
	ubRunGit(t, repoB, "worktree", "add", wtB, epicBranch)
	ubWriteFile(t, filepath.Join(wtB, "README.md"), "epic side\n")
	ubRunGit(t, wtB, "add", "README.md")
	ubRunGit(t, wtB, "commit", "-q", "-m", "epic side b")
	ubWriteFile(t, filepath.Join(repoB, "README.md"), "main side\n")
	ubRunGit(t, repoB, "add", "README.md")
	ubRunGit(t, repoB, "commit", "-q", "-m", "main side b")

	mainABefore := ubGitOut(t, e.repoDir, "rev-parse", "main")
	bMainBefore := ubGitOut(t, repoB, "rev-parse", "main")
	bEpicBefore := ubGitOut(t, repoB, "rev-parse", epicBranch)

	err = e.epicSvc.RequestMergeToMain(e.ctx, epicID)
	require.Error(t, err)
	var cf *EpicMergeConflictError
	require.True(t, errors.As(err, &cf), "want *EpicMergeConflictError, got %v", err)
	require.Equal(t, repoB, cf.RepoPath, "the conflict must name repo B")
	require.Contains(t, cf.ConflictFiles, "README.md")

	// Repo A fully merged: its main == the epic head.
	epicA := ubGitOut(t, e.repoDir, "rev-parse", epicBranch)
	require.Equal(t, epicA, ubGitOut(t, e.repoDir, "rev-parse", "main"), "repo A must complete its merge")
	require.NotEqual(t, mainABefore, epicA)

	// Repo B refs untouched.
	require.Equal(t, bMainBefore, ubGitOut(t, repoB, "rev-parse", "main"), "repo B main must not move on conflict")
	require.Equal(t, bEpicBefore, ubGitOut(t, repoB, "rev-parse", epicBranch), "repo B epic ref must not move on conflict")

	// The epic is escalated: gate_blocked + a reason naming the split.
	iss, err := e.q.GetIssue(e.ctx, epicID)
	require.NoError(t, err)
	require.Equal(t, "gate_blocked", iss.ExecStatus)
	require.Contains(t, iss.ExecStatusReason.String, "repo-b")
	require.Contains(t, iss.ExecStatusReason.String, "多仓合并部分失败")

	// Control workspace attention; no prompt while unresolved.
	ws, err := e.q.GetWorkspace(e.ctx, out.WorkspaceID)
	require.NoError(t, err)
	require.Equal(t, "attention", ws.Status)
	require.Equal(t, kickoffsBefore, len(proxy.kickoffs), "no prompt may be sent while the conflict is unresolved")
}

// TestEpicMergeToMain_WorktreeContentPulledBySync pins the sync step's effect
// in the simplest shape: with main AHEAD and the epic branch untouched by any
// pre-merge (main is an ancestor → step ① skipped), main fast-forwards to the
// epic head and the control worktree's branch tip equals it afterwards.
func TestEpicMergeToMain_WorktreeOnEpicBranchFollowsRef(t *testing.T) {
	e, _ := mtmEnv(t)
	epicID := e.makeEpic(t, "同步 epic")
	out, err := e.epicSvc.StartWorkspaceForIssue(e.ctx, epicID)
	require.NoError(t, err)
	wtPath := mtmControlWorktree(t, e, out.WorkspaceID)
	epicBranch := fmt.Sprintf("epic/%d", epicID)

	// Epic-side work only; main stays an ancestor → step ① skipped.
	ubWriteFile(t, filepath.Join(wtPath, "epic.txt"), "epic work\n")
	ubRunGit(t, wtPath, "add", "epic.txt")
	ubRunGit(t, wtPath, "commit", "-q", "-m", "epic advance")

	require.NoError(t, e.epicSvc.RequestMergeToMain(e.ctx, epicID))

	epicHead := ubGitOut(t, e.repoDir, "rev-parse", epicBranch)
	require.Equal(t, epicHead, ubGitOut(t, e.repoDir, "rev-parse", "main"))
	require.Equal(t, epicHead, ubGitOut(t, wtPath, "rev-parse", "HEAD"),
		"the control worktree sits ON epic/<id>, so its HEAD follows the merged branch")
	content, err := os.ReadFile(filepath.Join(wtPath, "epic.txt"))
	require.NoError(t, err)
	require.Equal(t, "epic work\n", string(content))
}
