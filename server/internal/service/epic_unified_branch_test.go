package service

// Epic 统一分支 Wave 1 (spec docs/superpowers/specs/2026-09-28-epic-unified-branch-design.md
// §1 分支统一 / §2 场景补全 b / 验收 2、3)。
//
// Unlike the fake-based epic_execution tests, these run the REAL
// WorkspaceService.Create against a real git repo on disk, so the worktree
// layout, the ws_repo.Branch discriminator and branch survival after archive
// are all pinned end-to-end.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// unifiedBranchEnv bundles the real services the unified-branch tests need:
// a real git repo on disk, a real WorkspaceService (Create runs actual
// `git worktree add`), and the Epic engine wired on top of it.
type unifiedBranchEnv struct {
	db       *sql.DB
	q        *store.Queries
	kanban   *KanbanService
	wsSvc    *WorkspaceService
	epicSvc  *EpicExecutionService
	repoID   int64
	repoDir  string
	columnID int64
	ctx      context.Context
}

func setupUnifiedBranchEnv(t *testing.T) *unifiedBranchEnv {
	t.Helper()
	ctx := context.Background()

	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	store.Driver = "sqlite"
	require.NoError(t, store.ApplySchema(db))
	store.Migrate(db)
	q := store.New(db)

	// Real git repo with one commit on main.
	repoDir := filepath.Join(t.TempDir(), "repo")
	ubRunGit(t, "", "init", "-q", "-b", "main", repoDir)
	ubRunGit(t, repoDir, "config", "user.email", "epic@example.com")
	ubRunGit(t, repoDir, "config", "user.name", "epic-test")
	ubRunGit(t, repoDir, "config", "commit.gpgsign", "false")
	ubWriteFile(t, filepath.Join(repoDir, "README.md"), "# repo\n")
	ubRunGit(t, repoDir, "add", "README.md")
	ubRunGit(t, repoDir, "commit", "-q", "-m", "init")

	// Project + column + repository row. The repository owner must match the
	// project owner or ListProjectRepositories filters it out.
	proj, err := q.CreateProject(ctx, store.CreateProjectParams{Name: "epic-unified", OwnerType: "user", OwnerID: 1})
	require.NoError(t, err)
	col, err := q.CreateColumn(ctx, store.CreateColumnParams{ProjectID: proj.ID, Name: "todo", Position: 0})
	require.NoError(t, err)
	repo, err := q.CreateRepository(ctx, store.CreateRepositoryParams{
		Name:          "repo",
		Path:          repoDir,
		DefaultBranch: sql.NullString{String: "main", Valid: true},
		OwnerType:     "user",
		OwnerID:       1,
	})
	require.NoError(t, err)
	require.NoError(t, q.InsertProjectRepository(ctx, store.InsertProjectRepositoryParams{
		ProjectID: proj.ID, RepositoryID: repo.ID, DefaultBranch: "main",
	}))

	dataDir := t.TempDir()
	cfg := &config.WorkspaceConfig{BaseDir: filepath.Join(dataDir, "workspaces")}
	require.NoError(t, os.MkdirAll(cfg.BaseDir, 0o755))
	wsSvc := NewWorkspaceService(q, db, cfg, dataDir, nil, nil)

	activitySvc := NewIssueActivityService(q)
	kanban := NewKanbanService(db, q, activitySvc, nil, nil)
	epicSvc := NewEpicExecutionService(db, q, kanban, wsSvc, nil)

	return &unifiedBranchEnv{
		db: db, q: q, kanban: kanban, wsSvc: wsSvc, epicSvc: epicSvc,
		repoID: repo.ID, repoDir: repoDir, columnID: col.ID, ctx: ctx,
	}
}

func (e *unifiedBranchEnv) makeEpic(t *testing.T, title string) int64 {
	t.Helper()
	d, err := e.kanban.CreateIssue(e.ctx, e.columnID, title, "", 0, 0, "", "", "", 0, nil, "epic", 0, nil, nil, 0)
	require.NoError(t, err)
	return d.ID
}

func (e *unifiedBranchEnv) makeChild(t *testing.T, epicID int64, title string) int64 {
	t.Helper()
	d, err := e.kanban.CreateIssue(e.ctx, e.columnID, title, "", 0, 0, "", "", "", 0, &epicID, "task", 0, nil, nil, 0)
	require.NoError(t, err)
	return d.ID
}

// makeTask creates a plain (non-epic, unparented) task issue.
func (e *unifiedBranchEnv) makeTask(t *testing.T, title string) int64 {
	t.Helper()
	d, err := e.kanban.CreateIssue(e.ctx, e.columnID, title, "", 0, 0, "", "", "", 0, nil, "task", 0, nil, nil, 0)
	require.NoError(t, err)
	return d.ID
}

// epicActiveWorkspace returns the epic's non-archived workspace, if any.
func (e *unifiedBranchEnv) epicActiveWorkspace(t *testing.T, epicID int64) (store.Workspace, bool) {
	t.Helper()
	wss, err := e.q.GetWorkspacesByIssue(e.ctx, sql.NullInt64{Int64: epicID, Valid: true})
	require.NoError(t, err)
	for _, w := range wss {
		if w.IsArchived == 0 {
			return w, true
		}
	}
	return store.Workspace{}, false
}

func ubRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	var cmd *exec.Cmd
	if dir == "" {
		cmd = exec.Command("git", args...)
	} else {
		cmd = exec.Command("git", append([]string{"-C", dir}, args...)...)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func ubGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func ubWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestEpicUnifiedBranch_ControlWorkspaceChecksOutEpicBranch pins spec §1: the
// epic's own workspace checks out the existing epic/<id> branch (no ws-<id>/
// fork), the ws_repo row records epic/<id>, and the diff base stays the real
// baseline (main).
func TestEpicUnifiedBranch_ControlWorkspaceChecksOutEpicBranch(t *testing.T) {
	e := setupUnifiedBranchEnv(t)
	epicID := e.makeEpic(t, "统一分支 epic")

	out, err := e.epicSvc.StartWorkspaceForIssue(e.ctx, epicID)
	require.NoError(t, err)

	wts, err := e.q.ListWorktrees(e.ctx, out.WorkspaceID)
	require.NoError(t, err)
	require.Len(t, wts, 1)
	wantBranch := fmt.Sprintf("epic/%d", epicID)
	require.Equal(t, wantBranch, wts[0].Branch, "ws_repo row Branch must be epic/<id>, not a ws-<id>/ fork")
	require.Equal(t, "main", wts[0].BaseBranch, "diff base stays the real baseline")

	require.Equal(t, wantBranch, ubGitOut(t, wts[0].WorktreePath, "symbolic-ref", "--short", "HEAD"),
		"the worktree must be checked out ON epic/<id>")
}

// TestEpicUnifiedBranch_ChildCascadeCreatesParentWorkspace pins spec §2b:
// creating a child's workspace when the parent epic has NO active workspace
// first cascades the parent control workspace into existence (checked out on
// epic/<id>), then creates the child forked from epic/<id>.
func TestEpicUnifiedBranch_ChildCascadeCreatesParentWorkspace(t *testing.T) {
	e := setupUnifiedBranchEnv(t)
	epicID := e.makeEpic(t, "级联父 epic")
	childID := e.makeChild(t, epicID, "子任务")

	_, ok := e.epicActiveWorkspace(t, epicID)
	require.False(t, ok, "precondition: epic has no workspace yet")

	out, err := e.epicSvc.StartWorkspaceForIssue(e.ctx, childID)
	require.NoError(t, err)

	// Parent control workspace was created by the cascade, on epic/<id>.
	parentWs, ok := e.epicActiveWorkspace(t, epicID)
	require.True(t, ok, "creating the child's workspace must cascade-create the parent control workspace")
	parentWts, err := e.q.ListWorktrees(e.ctx, parentWs.ID)
	require.NoError(t, err)
	require.Len(t, parentWts, 1)
	epicBranch := fmt.Sprintf("epic/%d", epicID)
	require.Equal(t, epicBranch, parentWts[0].Branch)
	require.Equal(t, epicBranch, ubGitOut(t, parentWts[0].WorktreePath, "symbolic-ref", "--short", "HEAD"))

	// The child exists, keeps its ws- prefixed fork, and forks from epic/<id>.
	childWts, err := e.q.ListWorktrees(e.ctx, out.WorkspaceID)
	require.NoError(t, err)
	require.Len(t, childWts, 1)
	require.True(t, strings.HasPrefix(childWts[0].Branch, "ws-"),
		"child branch %q must keep the ws- prefix (children unchanged)", childWts[0].Branch)
	epicHead := ubGitOut(t, e.repoDir, "rev-parse", epicBranch)
	mergeBase := ubGitOut(t, e.repoDir, "merge-base", childWts[0].Branch, epicBranch)
	require.Equal(t, epicHead, mergeBase, "child must fork from the epic/<id> head")
}

// TestEpicUnifiedBranch_ArchiveKeepsEpicBranch pins 验收 2: archiving a
// unified epic control workspace removes the worktree but NEVER deletes the
// epic/<id> branch the whole epic builds on.
func TestEpicUnifiedBranch_ArchiveKeepsEpicBranch(t *testing.T) {
	e := setupUnifiedBranchEnv(t)
	epicID := e.makeEpic(t, "归档保留 epic")
	out, err := e.epicSvc.StartWorkspaceForIssue(e.ctx, epicID)
	require.NoError(t, err)

	epicBranch := fmt.Sprintf("epic/%d", epicID)
	require.Contains(t, ubGitOut(t, e.repoDir, "branch", "--list", epicBranch), epicBranch,
		"precondition: epic branch exists")

	require.NoError(t, e.wsSvc.Archive(e.ctx, out.WorkspaceID))

	require.NotContains(t, ubGitOut(t, e.repoDir, "worktree", "list"), epicBranch,
		"the control worktree must be removed from the repo")
	require.Contains(t, ubGitOut(t, e.repoDir, "branch", "--list", epicBranch), epicBranch,
		"the epic/<id> branch must survive archiving the control workspace")
}

// TestEpicUnifiedBranch_CreateDelegatesForEpicManagedIssue pins spec §2b's
// HTTP 收口: WorkspaceService.Create on an epic-managed issue (child of an
// epic) delegates to the engine's creation path — the client's hand-picked
// branch ("main" here) is ignored and the worktree is forked from epic/<id>.
func TestEpicUnifiedBranch_CreateDelegatesForEpicManagedIssue(t *testing.T) {
	e := setupUnifiedBranchEnv(t)
	// Wire the production delegation (server.New does the same).
	e.wsSvc.SetEpicCreateDelegate(e.epicSvc.EpicCreateDelegate())

	epicID := e.makeEpic(t, "收口 epic")
	childID := e.makeChild(t, epicID, "收口子任务")

	// Give the epic branch real work so "forked from epic/<id>" is distinguishable
	// from "forked from main": commit directly on the control worktree (which sits
	// ON epic/<id> since the unification).
	out, err := e.epicSvc.StartWorkspaceForIssue(e.ctx, epicID)
	require.NoError(t, err)
	ctrlWs, err := e.q.GetWorkspace(e.ctx, out.WorkspaceID)
	require.NoError(t, err)
	ctrlWts, err := e.q.ListWorktrees(e.ctx, ctrlWs.ID)
	require.NoError(t, err)
	require.Len(t, ctrlWts, 1)
	ubWriteFile(t, filepath.Join(ctrlWts[0].WorktreePath, "epic.txt"), "epic work\n")
	ubRunGit(t, ctrlWts[0].WorktreePath, "add", "epic.txt")
	ubRunGit(t, ctrlWts[0].WorktreePath, "commit", "-q", "-m", "epic advance")
	epicBranch := fmt.Sprintf("epic/%d", epicID)
	epicHead := ubGitOut(t, e.repoDir, "rev-parse", epicBranch)
	require.NotEqual(t, epicHead, ubGitOut(t, e.repoDir, "rev-parse", "main"),
		"precondition: epic/<id> is ahead of main")

	// The plain (HTTP svc) create path with a client-chosen branch.
	res, err := e.wsSvc.Create(e.ctx, CreateWorkspaceInput{
		IssueID:   &childID,
		Name:      "client ws",
		Repos:     []RepoBranch{{RepoID: e.repoID, Branch: "main"}}, // 客户端手选 main
		OwnerType: "user",
		OwnerID:   1,
	})
	require.NoError(t, err)

	childWts, err := e.q.ListWorktrees(e.ctx, res.Workspace.ID)
	require.NoError(t, err)
	require.Len(t, childWts, 1)
	require.True(t, strings.HasPrefix(childWts[0].Branch, "ws-"),
		"child keeps its ws- prefixed fork: %q", childWts[0].Branch)
	mergeBase := ubGitOut(t, e.repoDir, "merge-base", childWts[0].Branch, epicBranch)
	require.Equal(t, epicHead, mergeBase,
		"the worktree must be forked from epic/<id>, NOT from the client-supplied main")

	// The engine's own path stays recursion-free with the delegate wired.
	out2, err := e.epicSvc.StartWorkspaceForIssue(e.ctx, childID)
	require.NoError(t, err, "engine path must not recurse through the delegate")
	require.Equal(t, res.Workspace.ID, out2.WorkspaceID,
		"engine path reuses the same (just-created) child workspace")

	// Delegation also applies to the epic issue itself: a plain Create on the
	// epic yields the unified control workspace (checked out on epic/<id>).
	epic2 := e.makeEpic(t, "收口 epic 本体")
	res2, err := e.wsSvc.Create(e.ctx, CreateWorkspaceInput{
		IssueID:   &epic2,
		Name:      "client epic ws",
		Repos:     []RepoBranch{{RepoID: e.repoID, Branch: "some-other-branch"}},
		OwnerType: "user",
		OwnerID:   1,
	})
	require.NoError(t, err)
	epic2Wts, err := e.q.ListWorktrees(e.ctx, res2.Workspace.ID)
	require.NoError(t, err)
	require.Len(t, epic2Wts, 1)
	require.Equal(t, fmt.Sprintf("epic/%d", epic2), epic2Wts[0].Branch,
		"a plain Create on an epic yields the unified control workspace")
}

// TestEpicUnifiedBranch_PlainTaskChildNotDelegated guards the §2b 收口 against
// over-delegation: epicIDForIssue marks ANY parented issue "managed" (the engine
// dispatch path relies on that), so the interception inside Create must
// re-check the parent's type. A child of a PLAIN task keeps the normal
// creation path — the user's hand-picked branch survives, no phantom
// epic/<parentID> branch is created, and the plain parent gets no cascade
// control workspace.
//
// The kanban layer derives issue_type from child presence (syncIssueType: any
// issue that gains a child is promoted to "epic"), so a task-typed parent row
// is normally unreachable through the service API. It IS reachable through raw
// / legacy writes that skip the sync — exactly the state the guard defends
// against — so the test forces the parent back to "task" directly in the store.
func TestEpicUnifiedBranch_PlainTaskChildNotDelegated(t *testing.T) {
	e := setupUnifiedBranchEnv(t)
	// Wire the production delegation (server.New does the same) — the guard must
	// hold WITH the delegate in place, not just in its absence.
	e.wsSvc.SetEpicCreateDelegate(e.epicSvc.EpicCreateDelegate())

	parentID := e.makeTask(t, "普通父任务")
	childID := e.makeChild(t, parentID, "普通父的子任务")
	// Simulate the invariant-violating row: parent has children but is a task.
	require.NoError(t, e.q.SetIssueType(e.ctx, store.SetIssueTypeParams{IssueType: "task", ID: parentID}))

	res, err := e.wsSvc.Create(e.ctx, CreateWorkspaceInput{
		IssueID:   &childID,
		Name:      "plain child ws",
		Repos:     []RepoBranch{{RepoID: e.repoID, Branch: "main"}}, // 客户端手选 main
		OwnerType: "user",
		OwnerID:   1,
	})
	require.NoError(t, err)

	childWts, err := e.q.ListWorktrees(e.ctx, res.Workspace.ID)
	require.NoError(t, err)
	require.Len(t, childWts, 1)
	require.Equal(t, fmt.Sprintf("ws-%d/main", res.Workspace.ID), childWts[0].Branch,
		"the child workspace must fork the user's hand-picked branch (main), NOT epic/<parentID>")
	require.Equal(t, "main", childWts[0].BaseBranch,
		"the diff base is the user-picked branch, not a phantom epic branch")

	epicBranch := fmt.Sprintf("epic/%d", parentID)
	require.NotContains(t, ubGitOut(t, e.repoDir, "branch", "--list", epicBranch), epicBranch,
		"no phantom epic/<plain-parent-id> branch may be created")

	pws, err := e.q.GetWorkspacesByIssue(e.ctx, sql.NullInt64{Int64: parentID, Valid: true})
	require.NoError(t, err)
	require.Empty(t, pws, "a plain task parent must not get a cascade control workspace")
}

// TestEpicUnifiedBranch_LegacyWsPrefixRowsStillDeleteBranchOnArchive guards
// the compat discriminator: a legacy workspace row whose Branch carries the
// ws-<id>/ prefix keeps the OLD archive behaviour (branch deleted).
func TestEpicUnifiedBranch_LegacyWsPrefixRowsStillDeleteBranchOnArchive(t *testing.T) {
	e := setupUnifiedBranchEnv(t)

	// Seed a workspace row + worktree the legacy way (ws-<id>/<branch>), as an
	// old epic control workspace would have on disk. `worktree add -b` creates
	// the branch, exactly like the old WorkspaceService.Create path did.
	ws, err := e.q.CreateWorkspace(e.ctx, store.CreateWorkspaceParams{
		Name: "legacy-epic-ws", Path: t.TempDir(), Status: "created", OwnerType: "user", OwnerID: 1,
	})
	require.NoError(t, err)
	legacyBranch := "ws-9/epic/9"
	wtPath := filepath.Join(ws.Path, ".worktrees", "legacy")
	require.NoError(t, os.MkdirAll(filepath.Dir(wtPath), 0o755))
	ubRunGit(t, e.repoDir, "worktree", "add", wtPath, "-b", legacyBranch, "main")
	_, err = e.q.CreateWorktree(e.ctx, store.CreateWorktreeParams{
		WorkspaceID: ws.ID, RepositoryID: e.repoID, WorktreePath: wtPath,
		Branch: legacyBranch, BaseBranch: "main",
	})
	require.NoError(t, err)

	require.NoError(t, e.wsSvc.Archive(e.ctx, ws.ID))

	require.NotContains(t, ubGitOut(t, e.repoDir, "branch", "--list", legacyBranch), legacyBranch,
		"legacy ws- prefixed rows keep the old archive behaviour (branch deleted)")
}

// TestEpicUnifiedBranch_DelegateHonorsDialogFields pins the delegation
// pass-through fix: Create on an epic-managed issue forwards the dialog's
// explicit CliType / MCPServers / Name / repo subset into the engine's
// creation path — the workspaces row and ws_repo rows honor them. Only the
// hand-picked BRANCH stays ignored (work still forks from epic/<id>).
func TestEpicUnifiedBranch_DelegateHonorsDialogFields(t *testing.T) {
	e := setupUnifiedBranchEnv(t)
	e.wsSvc.SetEpicCreateDelegate(e.epicSvc.EpicCreateDelegate())

	epicID := e.makeEpic(t, "透传 epic")
	childID := e.makeChild(t, epicID, "透传子任务")

	// A second project repo so "subset" is distinguishable from "all".
	repoB := mtmAddSecondRepo(t, e, "repo-b")
	require.NotEqual(t, e.repoID, repoB)

	res, err := e.wsSvc.Create(e.ctx, CreateWorkspaceInput{
		IssueID: &childID,
		Name:    "对话框自定义名",
		// Repo subset: repo A only — and a hand-picked branch that must be
		// ignored (the child still forks from epic/<id>).
		Repos:      []RepoBranch{{RepoID: e.repoID, Branch: "main"}},
		OwnerType:  "user",
		OwnerID:    1,
		MCPServers: []string{"exa", "context7"},
		CliType:    "codex",
	})
	require.NoError(t, err)

	ws, err := e.q.GetWorkspace(e.ctx, res.Workspace.ID)
	require.NoError(t, err)
	require.Equal(t, "对话框自定义名", ws.Name, "the dialog name must override issue.Title")
	require.Equal(t, "codex", ws.CliType, "the dialog cli pick must override the project default")
	require.Contains(t, ws.McpServers, "exa", "the dialog MCP selection must be persisted")
	require.Contains(t, ws.McpServers, "context7")

	// Repo subset honored; the branch is still epic-derived.
	wts, err := e.q.ListWorktrees(e.ctx, res.Workspace.ID)
	require.NoError(t, err)
	require.Len(t, wts, 1, "only the selected repo subset is attached")
	require.Equal(t, e.repoID, wts[0].RepositoryID, "repo A attached, repo B not")
	require.True(t, strings.HasPrefix(wts[0].Branch, "ws-"),
		"child keeps its ws- prefixed fork: %q", wts[0].Branch)
	epicBranch := fmt.Sprintf("epic/%d", epicID)
	epicHead := ubGitOut(t, e.repoDir, "rev-parse", epicBranch)
	require.Equal(t, epicHead, ubGitOut(t, e.repoDir, "merge-base", wts[0].Branch, epicBranch),
		"the fork point is epic/<id>, NOT the client-picked main")
}

// TestEpicUnifiedBranch_DelegateStaleSubsetKeepsFullSet pins the defensive
// half of the repo-subset filter: a client selection matching NO project repo
// falls back to the full set instead of producing a dead repo-less workspace.
func TestEpicUnifiedBranch_DelegateStaleSubsetKeepsFullSet(t *testing.T) {
	e := setupUnifiedBranchEnv(t)
	e.wsSvc.SetEpicCreateDelegate(e.epicSvc.EpicCreateDelegate())

	epicID := e.makeEpic(t, "陈旧子集 epic")
	childID := e.makeChild(t, epicID, "陈旧子集子任务")

	res, err := e.wsSvc.Create(e.ctx, CreateWorkspaceInput{
		IssueID: &childID,
		Name:    "stale subset",
		Repos:   []RepoBranch{{RepoID: 999999, Branch: "main"}}, // matches nothing
		OwnerType: "user",
		OwnerID:   1,
	})
	require.NoError(t, err)
	wts, err := e.q.ListWorktrees(e.ctx, res.Workspace.ID)
	require.NoError(t, err)
	require.Len(t, wts, 1, "a fully stale subset falls back to the full project repo set")
}
