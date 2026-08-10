package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initSourceWorkspace builds a fake niuniu workspace skeleton suitable for
// driving the WorktreeCreate hook against:
//
//	<root>/
//	├── CLAUDE.md
//	├── .mcp.json
//	├── .team/inboxes/.keep
//	└── .worktrees/<repoName>/      ← real git repo (one initial empty commit)
//
// `repoName` becomes the agent branch prefix (agent/<repoName>-<id>) so the
// test can assert on it. Returns the workspace root.
func initSourceWorkspace(t *testing.T, repoName string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("# fake claude.md\n"), 0o644); err != nil {
		t.Fatalf("write CLAUDE.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(`{"mcpServers":{}}`), 0o644); err != nil {
		t.Fatalf("write .mcp.json: %v", err)
	}
	teamDir := filepath.Join(root, ".team", "inboxes")
	if err := os.MkdirAll(teamDir, 0o755); err != nil {
		t.Fatalf("mkdir .team: %v", err)
	}
	if err := os.WriteFile(filepath.Join(teamDir, ".keep"), nil, 0o644); err != nil {
		t.Fatalf("write .keep: %v", err)
	}

	repoRoot := filepath.Join(root, ".worktrees", repoName)
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	mustGit(t, repoRoot, "init", "-q", "-b", "main")
	mustGit(t, repoRoot, "config", "user.email", "test@example.com")
	mustGit(t, repoRoot, "config", "user.name", "test")
	mustGit(t, repoRoot, "commit", "-q", "--allow-empty", "-m", "init")
	return root
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s in %s: %s: %v", strings.Join(args, " "), dir, strings.TrimSpace(string(out)), err)
	}
}

// runHook drives runWorktreeCreate / runWorktreeRemove with the given input
// payload and returns (exitCode, stdout, stderr). Tests rely on stdout
// being EXACTLY the worktree path (no trailing newline) so they assert
// against it directly.
func runHook(fn func(stdin, stdout, stderr *bytes.Buffer) int, input map[string]any) (int, string, string) {
	in := bytes.Buffer{}
	out := bytes.Buffer{}
	errb := bytes.Buffer{}
	json.NewEncoder(&in).Encode(input)
	rc := fn(&in, &out, &errb)
	return rc, out.String(), errb.String()
}

func wrappedRun(create bool) func(stdin, stdout, stderr *bytes.Buffer) int {
	if create {
		return func(stdin, stdout, stderr *bytes.Buffer) int {
			return runWorktreeCreate(stdin, stdout, stderr)
		}
	}
	return func(stdin, stdout, stderr *bytes.Buffer) int {
		return runWorktreeRemove(stdin, stdout, stderr)
	}
}

func TestWorktreeCreate_BuildsRealGitWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	source := initSourceWorkspace(t, "niuniu-main")
	isolated := filepath.Join(source, ".isolated-1")

	rc, stdout, stderr := runHook(wrappedRun(true), map[string]any{
		"session_id":    "abc12345-de67-89ab-cdef-000000000000",
		"cwd":           source,
		"worktree_path": isolated,
	})
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, stderr)
	}
	if stdout != isolated {
		t.Errorf("stdout want %q (no trailing newline), got %q", isolated, stdout)
	}

	// Assert: <isolated>/.worktrees/niuniu-main is a real git worktree on
	// branch agent/niuniu-main-<id> off the source repo's HEAD.
	subRepo := filepath.Join(isolated, ".worktrees", "niuniu-main")
	if _, err := os.Stat(filepath.Join(subRepo, ".git")); err != nil {
		t.Fatalf("isolated worktree missing .git: %v", err)
	}
	branchOut, err := exec.Command("git", "-C", subRepo, "rev-parse", "--abbrev-ref", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse: %s: %v", branchOut, err)
	}
	branch := strings.TrimSpace(string(branchOut))
	if !strings.HasPrefix(branch, "agent/niuniu-main-") {
		t.Errorf("isolated branch want agent/niuniu-main-* prefix, got %q", branch)
	}

	// Workspace-level files were copied.
	for _, name := range []string{"CLAUDE.md", ".mcp.json", filepath.Join(".team", "inboxes", ".keep")} {
		if _, err := os.Stat(filepath.Join(isolated, name)); err != nil {
			t.Errorf("workspace file %s not copied: %v", name, err)
		}
	}

	// Source marker written so future nested calls collapse back to this source.
	markerData, err := os.ReadFile(filepath.Join(isolated, sourceMarkerRel))
	if err != nil {
		t.Fatalf("source marker missing: %v", err)
	}
	if strings.TrimSpace(string(markerData)) != source {
		t.Errorf("source marker want %q, got %q", source, strings.TrimSpace(string(markerData)))
	}
}

func TestWorktreeCreate_NestedIsolationCollapsesToSource(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	source := initSourceWorkspace(t, "niuniu-main")

	// Level 1: agent A's isolated workspace.
	level1 := filepath.Join(source, ".isolated-A")
	if rc, _, stderr := runHook(wrappedRun(true), map[string]any{
		"session_id":    "session-A",
		"cwd":           source,
		"worktree_path": level1,
	}); rc != 0 {
		t.Fatalf("level1 rc=%d stderr=%s", rc, stderr)
	}

	// Level 2: sub-agent B spawned by agent A asks for its own isolation.
	// cwd is agent A's isolated workspace (level1) — without the source
	// marker handling, the new isolation would be created as a child of
	// level1, producing level1/.isolated-B/.worktrees/niuniu-main with
	// each level adding another layer.
	level2 := filepath.Join(source, ".isolated-B")
	rc, stdout, stderr := runHook(wrappedRun(true), map[string]any{
		"session_id":    "session-B",
		"cwd":           level1,
		"worktree_path": level2,
	})
	if rc != 0 {
		t.Fatalf("level2 rc=%d stderr=%s", rc, stderr)
	}
	if stdout != level2 {
		t.Errorf("level2 stdout want %q, got %q", level2, stdout)
	}

	// The level2 marker must point at the ORIGINAL source workspace, not
	// at level1 — that's the contract that keeps the directory hierarchy
	// flat instead of recursive.
	got, err := os.ReadFile(filepath.Join(level2, sourceMarkerRel))
	if err != nil {
		t.Fatalf("level2 marker missing: %v", err)
	}
	if strings.TrimSpace(string(got)) != source {
		t.Errorf("level2 source marker want %q (collapsed to original source), got %q",
			source, strings.TrimSpace(string(got)))
	}

	// Both isolated worktrees must be registered against the source repo
	// (siblings, not parent/child) — `git worktree list` lists all three.
	sourceRepo := filepath.Join(source, ".worktrees", "niuniu-main")
	listOut, err := exec.Command("git", "-C", sourceRepo, "worktree", "list", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatalf("worktree list: %s: %v", listOut, err)
	}
	listed := string(listOut)
	for _, want := range []string{
		filepath.ToSlash(filepath.Join(level1, ".worktrees", "niuniu-main")),
		filepath.ToSlash(filepath.Join(level2, ".worktrees", "niuniu-main")),
	} {
		if !strings.Contains(filepath.ToSlash(listed), want) {
			t.Errorf("worktree list missing %q:\n%s", want, listed)
		}
	}
}

func TestWorktreeRemove_DeregistersAndDeletes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	source := initSourceWorkspace(t, "niuniu-main")
	isolated := filepath.Join(source, ".isolated-1")

	if rc, _, stderr := runHook(wrappedRun(true), map[string]any{
		"session_id":    "session-X",
		"cwd":           source,
		"worktree_path": isolated,
	}); rc != 0 {
		t.Fatalf("create rc=%d stderr=%s", rc, stderr)
	}

	// Remove + assert clean.
	rc, _, _ := runHook(wrappedRun(false), map[string]any{
		"cwd":           source,
		"worktree_path": isolated,
	})
	if rc != 0 {
		t.Fatalf("remove rc=%d", rc)
	}
	if _, err := os.Stat(isolated); !os.IsNotExist(err) {
		t.Errorf("isolated dir still exists after remove: err=%v", err)
	}

	sourceRepo := filepath.Join(source, ".worktrees", "niuniu-main")
	listOut, err := exec.Command("git", "-C", sourceRepo, "worktree", "list", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatalf("worktree list: %s: %v", listOut, err)
	}
	if strings.Contains(string(listOut), filepath.ToSlash(filepath.Join(isolated, ".worktrees", "niuniu-main"))) {
		t.Errorf("registry still references removed worktree:\n%s", listOut)
	}
}

func TestWorktreeRemove_NeverBlocks(t *testing.T) {
	// WorktreeRemove cannot block per the docs. Even a totally invalid
	// payload must return 0; junk on stdin is silently swallowed.
	in := bytes.NewBufferString("not-json{")
	out := &bytes.Buffer{}
	errb := &bytes.Buffer{}
	if rc := runWorktreeRemove(in, out, errb); rc != 0 {
		t.Errorf("rc want 0 (cannot block), got %d (stderr=%s)", rc, errb.String())
	}
}

func TestWorktreeCreate_RejectsMissingFields(t *testing.T) {
	rc, _, stderr := runHook(wrappedRun(true), map[string]any{
		"session_id": "x",
		// no cwd, no worktree_path
	})
	if rc == 0 {
		t.Errorf("rc want non-zero on missing fields")
	}
	if !strings.Contains(stderr, "missing") {
		t.Errorf("stderr want 'missing' diagnostic, got %q", stderr)
	}
}

// TestShortID_StableOnSession asserts the prefix is derived from the
// session id (so branches are roughly traceable to a session) and that
// repeated calls still differ in the random tail (so consecutive spawns
// in the same session don't collide).
func TestShortID_StableOnSession(t *testing.T) {
	a := shortID("abc12345-zz")
	b := shortID("abc12345-zz")
	if !strings.HasPrefix(a, "abc12345-") || !strings.HasPrefix(b, "abc12345-") {
		t.Fatalf("prefix want abc12345-*, got %q / %q", a, b)
	}
	if a == b {
		t.Errorf("random tail should differ between calls; both %q", a)
	}
}

func TestShortID_FallsBackOnEmpty(t *testing.T) {
	a := shortID("")
	if a == "" {
		t.Fatal("shortID(empty) returned empty string")
	}
	if !strings.Contains(a, "-") {
		t.Errorf("shortID format should be <head>-<tail>, got %q", a)
	}
}

// TestResolveSourceWorkspace_FallsBackToCWD is the no-marker case — when
// no source marker exists at the cwd, the cwd itself IS the source, and
// the helper returns it unchanged.
func TestResolveSourceWorkspace_FallsBackToCWD(t *testing.T) {
	dir := t.TempDir()
	if got := resolveSourceWorkspace(dir); got != dir {
		t.Errorf("want %q (no marker → cwd), got %q", dir, got)
	}
}

func TestResolveSourceWorkspace_HonorsMarker(t *testing.T) {
	cwd := t.TempDir()
	source := t.TempDir()
	markerDir := filepath.Join(cwd, ".workspace-hooks")
	if err := os.MkdirAll(markerDir, 0o755); err != nil {
		t.Fatalf("mkdir marker dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(markerDir, "source"), []byte(source+"\n"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	got := resolveSourceWorkspace(cwd)
	if got != source {
		t.Errorf("want %q (marker wins), got %q", source, got)
	}
}
