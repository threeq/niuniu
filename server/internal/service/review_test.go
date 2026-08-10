package service

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/niuniu-dev/niuniu/internal/git"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// fakeAgentProxy records the calls SendCommentToAgent makes so the test can
// assert the comment is routed through the agentproxy (chat) path rather than
// the PTY AgentManager.
type fakeAgentProxy struct {
	preparedWS  int64
	deliverWS   int64
	deliverDir  string
	deliverBody string
	deliverErr  error
}

func (f *fakeAgentProxy) GetOrStartSession(context.Context, int64, int64) (AgentSession, error) {
	return nil, nil
}
func (f *fakeAgentProxy) GetSession(int64) AgentSession               { return nil }
func (f *fakeAgentProxy) PrepareUserSend(_ context.Context, ws int64) { f.preparedWS = ws }
func (f *fakeAgentProxy) Deliver(_ context.Context, ws int64, workDir, content, _ string) (bool, int64, error) {
	f.deliverWS = ws
	f.deliverDir = workDir
	f.deliverBody = content
	return false, 0, f.deliverErr
}

// SendCommentToAgent must route a review comment through the agentproxy chat
// session (Deliver), not the PTY AgentManager — the regression behind
// "send to agent: no agent running for workspace N" when reviewing in focus mode.
func TestSendCommentToAgentRoutesThroughProxy(t *testing.T) {
	svc, q := setupReviewTest(t)
	ctx := context.Background()

	ws, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
		Name:      "ws",
		Path:      "/tmp/ws-path",
		Status:    "active",
		OwnerType: "user",
		OwnerID:   1,
		CliType:   "claude",
	})
	if err != nil {
		t.Fatal(err)
	}

	comment, err := svc.CreateComment(ctx, ws.ID, CreateCommentInput{
		Repo:     "niuniu",
		FilePath: "a.go",
		Content:  "please fix this",
	})
	if err != nil {
		t.Fatal(err)
	}

	fp := &fakeAgentProxy{}
	svc.SetAgentProxy(fp)

	if err := svc.SendCommentToAgent(ctx, comment.ID); err != nil {
		t.Fatalf("SendCommentToAgent: %v", err)
	}

	if fp.deliverWS != ws.ID {
		t.Errorf("delivered to workspace %d, want %d", fp.deliverWS, ws.ID)
	}
	if fp.deliverDir != ws.Path {
		t.Errorf("delivered with workDir %q, want %q", fp.deliverDir, ws.Path)
	}
	if fp.preparedWS != ws.ID {
		t.Errorf("PrepareUserSend got workspace %d, want %d", fp.preparedWS, ws.ID)
	}
	if !strings.Contains(fp.deliverBody, "please fix this") || !strings.Contains(fp.deliverBody, "a.go") {
		t.Errorf("delivered body missing comment/location: %q", fp.deliverBody)
	}

	got, err := q.GetComment(ctx, comment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.SentToAgent.Bool {
		t.Error("comment not marked sent_to_agent after delivery")
	}
}

func TestRepoMatchesSource(t *testing.T) {
	repo := func(remote, path string) store.Repository {
		return store.Repository{
			GitRemote: sql.NullString{String: remote, Valid: remote != ""},
			Path:      path,
		}
	}
	cases := []struct {
		name string
		repo store.Repository
		src  git.WorktreeSource
		want bool
	}{
		{"remote match", repo("https://x/r.git", "/p"), git.WorktreeSource{RemoteURL: "https://x/r.git", SourcePath: "/other"}, true},
		{"path match", repo("https://x/r.git", "/p"), git.WorktreeSource{RemoteURL: "https://y/z.git", SourcePath: "/p"}, true},
		{"no match", repo("https://x/r.git", "/p"), git.WorktreeSource{RemoteURL: "https://y/z.git", SourcePath: "/q"}, false},
		{"empty src never matches empty repo remote", repo("", ""), git.WorktreeSource{}, false},
		{"empty src remote does not match via blank", repo("", "/p"), git.WorktreeSource{RemoteURL: "", SourcePath: ""}, false},
		{"invalid repo remote falls through to path", repo("", "/p"), git.WorktreeSource{RemoteURL: "https://x/r.git", SourcePath: "/p"}, true},
	}
	for _, c := range cases {
		if got := repoMatchesSource(c.repo, c.src); got != c.want {
			t.Errorf("%s: repoMatchesSource = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSummariseDiffs(t *testing.T) {
	in := []git.FileDiff{
		{Path: "a.txt", Status: "modified", Additions: 3, Deletions: 1,
			Hunks: []git.DiffHunk{{OldStart: 1}}, RawPatch: "diff --git a/a.txt b/a.txt\n..."},
	}
	out := summariseDiffs(in)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	if out[0].Hunks != nil || out[0].RawPatch != "" {
		t.Errorf("line-level data not stripped: hunks=%v raw=%q", out[0].Hunks, out[0].RawPatch)
	}
	// Summary fields are preserved.
	if out[0].Path != "a.txt" || out[0].Additions != 3 || out[0].Deletions != 1 {
		t.Errorf("summary fields lost: %+v", out[0])
	}
	// Input is not mutated (defensive copy).
	if in[0].RawPatch == "" {
		t.Error("summariseDiffs mutated its input")
	}
}

// --- GetDiff / GetRepoDiff integration ---

func setupReviewTest(t *testing.T) (*ReviewService, *store.Queries) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	// No _foreign_keys=ON: lets us insert worktrees with a dangling repository_id
	// to represent the "stored association went stale" production state.
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatal(err)
	}
	q := store.New(db)
	return NewReviewService(q), q
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// initWorktree creates a real git repo with one committed file + one untracked
// file (so the "vs main" diff is non-empty), optionally with an origin remote.
func initWorktree(t *testing.T, remote string) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "-b", "main", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	run := func(args ...string) {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("config", "user.email", "t@e.com")
	run("config", "user.name", "t")
	run("config", "commit.gpgsign", "false")
	if remote != "" {
		run("remote", "add", "origin", remote)
	}
	mustWriteFile(t, filepath.Join(dir, "tracked.txt"), "a\n")
	run("add", "-A")
	run("commit", "-q", "-m", "init")
	mustWriteFile(t, filepath.Join(dir, "untracked.txt"), "hello\n")
	return dir
}

func TestGetDiff(t *testing.T) {
	svc, q := setupReviewTest(t)
	ctx := context.Background()

	mkWorkspace := func(ownerID int64) int64 {
		ws, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
			Name: "ws", Path: t.TempDir(), Status: "created", OwnerType: "user", OwnerID: ownerID,
		})
		if err != nil {
			t.Fatal(err)
		}
		return ws.ID
	}
	mkRepo := func(name, remote, path string, ownerID int64) store.Repository {
		r, err := q.CreateRepository(ctx, store.CreateRepositoryParams{
			Name: name, Path: path, GitRemote: sql.NullString{String: remote, Valid: true},
			OwnerType: "user", OwnerID: ownerID,
		})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	mkWorktree := func(wsID, repoID int64, dir string) {
		if _, err := q.CreateWorktree(ctx, store.CreateWorktreeParams{
			WorkspaceID: wsID, RepositoryID: repoID, WorktreePath: dir, Branch: "main", BaseBranch: "main",
		}); err != nil {
			t.Fatal(err)
		}
	}
	getGroup := func(wsID int64) RepoDiff {
		groups, err := svc.GetDiff(ctx, wsID)
		if err != nil {
			t.Fatalf("GetDiff: %v", err)
		}
		if len(groups) != 1 {
			t.Fatalf("got %d groups, want 1", len(groups))
		}
		return groups[0]
	}

	t.Run("healthy association resolves and ships summary-only", func(t *testing.T) {
		dir := initWorktree(t, "https://example.com/acme/healthy.git")
		repo := mkRepo("healthy", "https://example.com/acme/healthy.git", dir, 1)
		ws := mkWorkspace(1)
		mkWorktree(ws, repo.ID, dir)

		g := getGroup(ws)
		if g.RepositoryID != repo.ID {
			t.Errorf("RepositoryID = %d, want %d", g.RepositoryID, repo.ID)
		}
		if len(g.Files) == 0 {
			t.Fatal("expected changed files (untracked.txt)")
		}
		if g.Files[0].RawPatch != "" || g.Files[0].Hunks != nil {
			t.Errorf("resolved group must be summary-only, got raw=%q", g.Files[0].RawPatch)
		}
	})

	t.Run("stale association reverse-resolves to same-owner repo by remote", func(t *testing.T) {
		remote := "https://example.com/acme/stale.git"
		dir := initWorktree(t, remote)
		repo := mkRepo("stale", remote, "/some/other/path", 1) // path differs; match must be via remote
		ws := mkWorkspace(1)
		mkWorktree(ws, 999999, dir) // dangling repository_id

		g := getGroup(ws)
		if g.RepositoryID != repo.ID {
			t.Errorf("reverse-resolved RepositoryID = %d, want %d", g.RepositoryID, repo.ID)
		}
	})

	t.Run("owner-scoped: a foreign-owner repo is NOT matched", func(t *testing.T) {
		remote := "https://example.com/acme/foreign.git"
		dir := initWorktree(t, remote)
		mkRepo("foreign", remote, dir, 2) // matching remote AND path, but owner=2
		ws := mkWorkspace(1)              // workspace owned by user 1
		mkWorktree(ws, 999998, dir)

		g := getGroup(ws)
		if g.RepositoryID != 0 {
			t.Errorf("foreign-owner repo must not be matched; RepositoryID = %d, want 0", g.RepositoryID)
		}
		// Unmatched groups keep full line-level data for inline rendering.
		if len(g.Files) == 0 || g.Files[0].RawPatch == "" {
			t.Errorf("unmatched group must retain raw_patch for inline diff, got %d files", len(g.Files))
		}
	})

	t.Run("no match: repository_id 0 with directory basename", func(t *testing.T) {
		dir := initWorktree(t, "https://example.com/acme/unregistered.git")
		ws := mkWorkspace(1)
		mkWorktree(ws, 999997, dir)

		g := getGroup(ws)
		if g.RepositoryID != 0 {
			t.Errorf("RepositoryID = %d, want 0", g.RepositoryID)
		}
		if g.Name != filepath.Base(dir) {
			t.Errorf("Name = %q, want dir basename %q", g.Name, filepath.Base(dir))
		}
	})
}

func TestGetRepoDiff(t *testing.T) {
	svc, q := setupReviewTest(t)
	ctx := context.Background()

	t.Run("direct association", func(t *testing.T) {
		dir := initWorktree(t, "https://example.com/acme/direct.git")
		repo, err := q.CreateRepository(ctx, store.CreateRepositoryParams{
			Name: "direct", Path: dir, GitRemote: sql.NullString{String: "https://example.com/acme/direct.git", Valid: true},
			OwnerType: "user", OwnerID: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		ws, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{Name: "ws", Path: t.TempDir(), Status: "created", OwnerType: "user", OwnerID: 1})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := q.CreateWorktree(ctx, store.CreateWorktreeParams{WorkspaceID: ws.ID, RepositoryID: repo.ID, WorktreePath: dir, Branch: "main", BaseBranch: "main"}); err != nil {
			t.Fatal(err)
		}

		files, err := svc.GetRepoDiff(ctx, ws.ID, repo.ID)
		if err != nil {
			t.Fatalf("GetRepoDiff: %v", err)
		}
		if len(files) == 0 {
			t.Fatal("expected files")
		}
		// Line-level endpoint returns full diffs (raw_patch present).
		if files[0].RawPatch == "" {
			t.Error("GetRepoDiff must return full diffs (raw_patch)")
		}
	})

	t.Run("fallback by source repo when no direct row", func(t *testing.T) {
		remote := "https://example.com/acme/fallback.git"
		dir := initWorktree(t, remote)
		repo, err := q.CreateRepository(ctx, store.CreateRepositoryParams{
			Name: "fallback", Path: dir, GitRemote: sql.NullString{String: remote, Valid: true},
			OwnerType: "user", OwnerID: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		ws, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{Name: "ws", Path: t.TempDir(), Status: "created", OwnerType: "user", OwnerID: 1})
		if err != nil {
			t.Fatal(err)
		}
		// Worktree row references a dangling id, so there is no (ws, repo.ID) row.
		if _, err := q.CreateWorktree(ctx, store.CreateWorktreeParams{WorkspaceID: ws.ID, RepositoryID: 888888, WorktreePath: dir, Branch: "main", BaseBranch: "main"}); err != nil {
			t.Fatal(err)
		}

		files, err := svc.GetRepoDiff(ctx, ws.ID, repo.ID)
		if err != nil {
			t.Fatalf("GetRepoDiff fallback: %v", err)
		}
		if len(files) == 0 {
			t.Fatal("expected files via source-repo fallback")
		}
	})
}
