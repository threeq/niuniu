package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// TestHasDanglingGitlink pins the detector that distinguishes a structurally
// broken worktree (gitlink whose target directory is missing — production
// case) from worktrees git can't read for other reasons (transient lock,
// permission denied, missing git binary). Only the gitlink-dangling case
// becomes a 200 broken=true response; everything else falls through and
// surfaces as a 5xx via the error path.
func TestHasDanglingGitlink(t *testing.T) {
	t.Run("dangling gitlink — production case", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /nonexistent/parent/.git/worktrees/x\n"), 0o644); err != nil {
			t.Fatalf("write .git: %v", err)
		}
		if !hasDanglingGitlink(dir) {
			t.Error("expected dangling gitlink to be detected")
		}
	})

	t.Run("gitlink target exists", func(t *testing.T) {
		dir := t.TempDir()
		target := t.TempDir() // exists
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+target+"\n"), 0o644); err != nil {
			t.Fatalf("write .git: %v", err)
		}
		if hasDanglingGitlink(dir) {
			t.Error("expected resolvable gitlink to NOT be flagged broken")
		}
	})

	t.Run("regular .git directory (full repo)", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if hasDanglingGitlink(dir) {
			t.Error("a directory-style .git is a normal repo, not a broken worktree")
		}
	})

	t.Run("no .git at all", func(t *testing.T) {
		if hasDanglingGitlink(t.TempDir()) {
			t.Error("missing .git is not a 'broken' state — it's just not a worktree")
		}
	})

	t.Run(".git file without gitdir line", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("# stray file\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if hasDanglingGitlink(dir) {
			t.Error("malformed .git file should not be classified as broken-worktree")
		}
	})
}

// makeBrokenWorktreeWorkspace inserts a workspace row whose Path resolves to a
// real directory containing a worktree subdir whose `.git` gitlink points to a
// non-existent parent repo. Mirrors the production state seen on
// team.niu6ai.com where the user's parent repository directory was removed
// while the workspace's worktree gitlink survived.
func makeBrokenWorktreeWorkspace(t *testing.T, q *store.Queries, name string) (workspaceID int64, worktreeName string) {
	t.Helper()
	wsDir := t.TempDir()
	wt := "broken-wt"
	wtPath := filepath.Join(wsDir, ".worktrees", wt)
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	// Gitlink points to a path that doesn't exist — git status will fail with
	// "fatal: not a git repository: <path>".
	if err := os.WriteFile(filepath.Join(wtPath, ".git"), []byte("gitdir: /nonexistent/parent/.git/worktrees/"+wt+"\n"), 0o644); err != nil {
		t.Fatalf("write .git: %v", err)
	}

	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name:      name,
		Path:      wsDir,
		Status:    "created",
		OwnerType: "user",
		OwnerID:   1,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	return ws.ID, wt
}

func newWorkspaceServiceForGitStatus(t *testing.T, q *store.Queries) *WorkspaceService {
	t.Helper()
	dataDir := t.TempDir()
	cfg := &config.WorkspaceConfig{BaseDir: filepath.Join(dataDir, "workspaces")}
	if err := os.MkdirAll(cfg.BaseDir, 0o755); err != nil {
		t.Fatalf("mkdir base: %v", err)
	}
	return NewWorkspaceService(q, nil, cfg, dataDir, nil, nil)
}

// TestGetWorktreeGitStatus_BrokenWorktreeReturnsBrokenFlag verifies the
// production bug fix: when a worktree directory exists on disk but `git
// status` fails (e.g. dangling gitlink because the parent repo was moved or
// deleted), the service returns a result with Broken=true rather than
// surfacing a generic error that the handler converts into a 404.
//
// Old behavior (the bug): handler returned 404 NotFound, frontend logged a
// "Failed to load resource" error in the network panel on every page load.
// New behavior: handler returns 200 with {broken: true, reason: "..."} so
// the frontend can render a "broken worktree" badge instead of erroring.
func TestGetWorktreeGitStatus_BrokenWorktreeReturnsBrokenFlag(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}

	db := openWorkspaceTestDB(t)
	q := store.New(db)
	wsID, wtName := makeBrokenWorktreeWorkspace(t, q, "broken-ws")
	svc := newWorkspaceServiceForGitStatus(t, q)

	got, err := svc.GetWorktreeGitStatus(context.Background(), wsID, wtName)
	if err != nil {
		t.Fatalf("expected no error for broken worktree (handler should not 404), got: %v", err)
	}
	if got == nil {
		t.Fatal("expected a non-nil GitStatusResult, got nil")
	}
	if !got.Broken {
		t.Errorf("expected Broken=true, got %+v", got)
	}
	if got.Reason == "" {
		t.Error("expected non-empty Reason explaining why git status failed")
	}
	// Empty change lists — broken worktree has no observable diffs.
	if len(got.Modified)+len(got.Added)+len(got.Deleted)+len(got.Untracked) != 0 {
		t.Errorf("expected empty change lists for broken worktree, got %+v", got)
	}
}

// TestGetWorktreeGitStatus_MissingPathStillErrors verifies that the
// "directory not on disk at all" case still surfaces an error (handler will
// 404). This distinguishes "broken but salvageable worktree" (200+broken)
// from "worktree row in DB but nothing on disk" (404 — a real not-found).
func TestGetWorktreeGitStatus_MissingPathStillErrors(t *testing.T) {
	db := openWorkspaceTestDB(t)
	q := store.New(db)

	// Workspace whose path exists but has NO .worktrees subdir.
	wsDir := t.TempDir()
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name:      "no-worktree",
		Path:      wsDir,
		Status:    "created",
		OwnerType: "user",
		OwnerID:   1,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	svc := newWorkspaceServiceForGitStatus(t, q)
	_, err = svc.GetWorktreeGitStatus(context.Background(), ws.ID, "ghost-wt")
	if err == nil {
		t.Fatal("expected error for genuinely missing worktree directory, got nil")
	}
}

// TestSidebarGitStatus_ReportsPerWorkspaceCounts verifies the lazy sidebar-git
// fan-out (方案 B): given a workspace with a real worktree that has an
// uncommitted change, sidebarGitStatus returns that workspace's aggregate
// changes_count plus the per-worktree breakdown.
func TestSidebarGitStatus_ReportsPerWorkspaceCounts(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}

	db := openWorkspaceTestDB(t)
	q := store.New(db)

	parentRepo := filepath.Join(t.TempDir(), "parent-repo")
	initRepoOnBranch(t, parentRepo, "main")

	wsDir := t.TempDir()
	wtPath := filepath.Join(wsDir, ".worktrees", "wt1")
	if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	mustGit(t, parentRepo, "worktree", "add", "-b", "feat", wtPath)
	// One untracked file => one change in git status --porcelain.
	if err := os.WriteFile(filepath.Join(wtPath, "new.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}

	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name:      "lazy-git-ws",
		Path:      wsDir,
		Status:    "created",
		OwnerType: "user",
		OwnerID:   1,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	svc := newWorkspaceServiceForGitStatus(t, q)
	got := svc.sidebarGitStatus(context.Background(), []store.Workspace{ws})
	if len(got) != 1 {
		t.Fatalf("expected 1 workspace status, got %d", len(got))
	}
	if got[0].WorkspaceID != ws.ID {
		t.Errorf("workspace_id = %d, want %d", got[0].WorkspaceID, ws.ID)
	}
	if got[0].ChangesCount < 1 {
		t.Errorf("changes_count = %d, want >= 1 (one untracked file)", got[0].ChangesCount)
	}
	if len(got[0].Worktrees) != 1 || got[0].Worktrees[0].Name != "wt1" {
		t.Errorf("expected one worktree named wt1, got %+v", got[0].Worktrees)
	}
	if got[0].Worktrees[0].ChangesCount < 1 {
		t.Errorf("worktree changes_count = %d, want >= 1", got[0].Worktrees[0].ChangesCount)
	}
}

// TestGetWorktreeGitStatus_HealthyWorktreeUnchanged verifies the happy path:
// a real worktree on a real parent repo returns a normal (non-broken) result.
func TestGetWorktreeGitStatus_HealthyWorktreeUnchanged(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}

	db := openWorkspaceTestDB(t)
	q := store.New(db)

	// Create a real parent repo and a worktree from it under a workspace dir.
	parentRepo := filepath.Join(t.TempDir(), "parent-repo")
	initRepoOnBranch(t, parentRepo, "main")

	wsDir := t.TempDir()
	wtPath := filepath.Join(wsDir, ".worktrees", "wt1")
	if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	mustGit(t, parentRepo, "worktree", "add", "-b", "feat", wtPath)

	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name:      "healthy-ws",
		Path:      wsDir,
		Status:    "created",
		OwnerType: "user",
		OwnerID:   1,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	svc := newWorkspaceServiceForGitStatus(t, q)
	got, err := svc.GetWorktreeGitStatus(context.Background(), ws.ID, "wt1")
	if err != nil {
		t.Fatalf("unexpected error on healthy worktree: %v", err)
	}
	if got.Broken {
		t.Errorf("expected Broken=false on healthy worktree, got %+v", got)
	}
}
