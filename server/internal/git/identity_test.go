package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// withTempGitConfig redirects --global git config to a temp file for the test
// so we never touch the developer's real ~/.gitconfig. The GIT_CONFIG_GLOBAL
// env var is honoured by git 2.32+.
func withTempGitConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitconfig")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatalf("seed gitconfig: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", path)
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	return path
}

func TestGetGlobalIdentity_Empty(t *testing.T) {
	withTempGitConfig(t)
	name, email, err := GetGlobalIdentity(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if name != "" || email != "" {
		t.Fatalf("expected empty, got name=%q email=%q", name, email)
	}
}

func TestGetGlobalIdentity_Set(t *testing.T) {
	cfg := withTempGitConfig(t)
	if err := os.WriteFile(cfg, []byte(`[user]
    name = Alice Dev
    email = alice@example.com
`), 0o644); err != nil {
		t.Fatalf("write cfg: %v", err)
	}
	name, email, err := GetGlobalIdentity(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if name != "Alice Dev" || email != "alice@example.com" {
		t.Fatalf("got name=%q email=%q", name, email)
	}
}

func TestSetGlobalIdentity_Valid(t *testing.T) {
	cfg := withTempGitConfig(t)
	err := SetGlobalIdentity(context.Background(), "Bob Dev", "bob@example.com")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "Bob Dev") || !strings.Contains(string(got), "bob@example.com") {
		t.Fatalf("config missing identity, got:\n%s", got)
	}
}

func TestSetGlobalIdentity_RejectsEmptyName(t *testing.T) {
	withTempGitConfig(t)
	err := SetGlobalIdentity(context.Background(), "", "x@y.z")
	if !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("expected ErrInvalidIdentity, got %v", err)
	}
}

func TestSetGlobalIdentity_RejectsBadEmail(t *testing.T) {
	withTempGitConfig(t)
	err := SetGlobalIdentity(context.Background(), "Bob", "not-an-email")
	if !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("expected ErrInvalidIdentity, got %v", err)
	}
}

func TestSetGlobalIdentity_RejectsControlChars(t *testing.T) {
	withTempGitConfig(t)
	if err := SetGlobalIdentity(context.Background(), "Alice\nEvil", "a@b.c"); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("expected ErrInvalidIdentity for newline in name, got %v", err)
	}
	if err := SetGlobalIdentity(context.Background(), "Alice", "a\nb@c.d"); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("expected ErrInvalidIdentity for newline in email, got %v", err)
	}
	if err := SetGlobalIdentity(context.Background(), "Alice\rEvil", "a@b.c"); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("expected ErrInvalidIdentity for CR in name, got %v", err)
	}
	if err := SetGlobalIdentity(context.Background(), "Alice\x00", "a@b.c"); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("expected ErrInvalidIdentity for NUL in name, got %v", err)
	}
}

func setupRepoWithIdentity(t *testing.T) string {
	t.Helper()
	withTempGitConfig(t)
	if err := SetGlobalIdentity(context.Background(), "T", "t@e.com"); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	dir := t.TempDir()
	cmd := exec.Command("git", "-C", dir, "init")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	return dir
}

func headExists(t *testing.T, dir string) bool {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--verify", "HEAD")
	return cmd.Run() == nil
}

func TestEnsureInitialCommit_NoOpWhenHeadExists(t *testing.T) {
	dir := setupRepoWithIdentity(t)
	// First commit
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, dir, "git", "add", "-A")
	mustRun(t, dir, "git", "commit", "-m", "first")
	if err := EnsureInitialCommit(context.Background(), dir, Identity{}); err != nil {
		t.Fatalf("EnsureInitialCommit: %v", err)
	}
	// Still exactly 1 commit
	out, _ := exec.Command("git", "-C", dir, "rev-list", "--count", "HEAD").Output()
	if strings.TrimSpace(string(out)) != "1" {
		t.Fatalf("expected 1 commit, got %s", out)
	}
}

func TestEnsureInitialCommit_EmptyDirectory(t *testing.T) {
	dir := setupRepoWithIdentity(t)
	if err := EnsureInitialCommit(context.Background(), dir, Identity{}); err != nil {
		t.Fatalf("EnsureInitialCommit: %v", err)
	}
	if !headExists(t, dir) {
		t.Fatal("HEAD not created")
	}
	if _, err := os.Stat(filepath.Join(dir, ".keep-niuniu")); err != nil {
		t.Fatalf(".keep-niuniu not present: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, ".keep-niuniu"))
	want := "# placeholder created by niuniu on first init; safe to delete after adding real content\n"
	if string(body) != want {
		t.Fatalf(".keep-niuniu body wrong:\n%q\nwant:\n%q", body, want)
	}
	out, _ := exec.Command("git", "-C", dir, "log", "-1", "--format=%s").Output()
	if strings.TrimSpace(string(out)) != "chore: niuniu init" {
		t.Fatalf("commit msg = %q", out)
	}
}

func TestEnsureInitialCommit_DirectoryWithFiles(t *testing.T) {
	dir := setupRepoWithIdentity(t)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureInitialCommit(context.Background(), dir, Identity{}); err != nil {
		t.Fatalf("EnsureInitialCommit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".keep-niuniu")); !os.IsNotExist(err) {
		t.Fatalf(".keep-niuniu should NOT exist when dir has files")
	}
	out, _ := exec.Command("git", "-C", dir, "log", "-1", "--name-only", "--format=").Output()
	if !strings.Contains(string(out), "README.md") {
		t.Fatalf("first commit should include README.md, got %s", out)
	}
}

func TestEnsureInitialCommit_IdentityMissing(t *testing.T) {
	withTempGitConfig(t) // no identity seeded
	dir := t.TempDir()
	mustRun(t, dir, "git", "init")
	err := EnsureInitialCommit(context.Background(), dir, Identity{})
	if !errors.Is(err, ErrIdentityMissing) {
		t.Fatalf("expected ErrIdentityMissing, got %v", err)
	}
}

// --- CommitAs tests (Phase 0 per-user identity) ---

// setupBareInitRepo inits a repo with NO identity configured. CommitAs with
// a non-zero Identity{} should still work because `-c user.*` overrides
// repo/global config.
func setupBareInitRepo(t *testing.T) string {
	t.Helper()
	withTempGitConfig(t) // no identity seeded
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	return dir
}

func TestCommitAs_OverridesIdentity(t *testing.T) {
	dir := setupBareInitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	id := Identity{Name: "Alice Niu", Email: "alice@niuniu.local"}
	if err := CommitAs(context.Background(), dir, id, "first", true); err != nil {
		t.Fatalf("CommitAs: %v", err)
	}
	out, _ := exec.Command("git", "-C", dir, "log", "-1", "--format=%an <%ae>").Output()
	got := strings.TrimSpace(string(out))
	if got != "Alice Niu <alice@niuniu.local>" {
		t.Fatalf("author = %q, want %q", got, "Alice Niu <alice@niuniu.local>")
	}
}

func TestCommitAs_ZeroIdentity_UsesRepoConfig(t *testing.T) {
	dir := setupRepoWithIdentity(t) // seeds global "T <t@e.com>"
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CommitAs(context.Background(), dir, Identity{}, "first", true); err != nil {
		t.Fatalf("CommitAs: %v", err)
	}
	out, _ := exec.Command("git", "-C", dir, "log", "-1", "--format=%an <%ae>").Output()
	got := strings.TrimSpace(string(out))
	if got != "T <t@e.com>" {
		t.Fatalf("author = %q, want %q", got, "T <t@e.com>")
	}
}

func TestCommitAs_ZeroIdentity_NoConfig_ReturnsErrIdentityMissing(t *testing.T) {
	dir := setupBareInitRepo(t)
	err := CommitAs(context.Background(), dir, Identity{}, "first", true)
	if !errors.Is(err, ErrIdentityMissing) {
		t.Fatalf("expected ErrIdentityMissing, got %v", err)
	}
}

func TestCommitAs_RejectsPartialIdentity(t *testing.T) {
	dir := setupBareInitRepo(t)
	cases := []Identity{
		{Name: "Alice", Email: ""},
		{Name: "", Email: "a@b.c"},
	}
	for _, id := range cases {
		err := CommitAs(context.Background(), dir, id, "first", true)
		if !errors.Is(err, ErrInvalidIdentity) {
			t.Errorf("%+v: expected ErrInvalidIdentity, got %v", id, err)
		}
	}
}

func TestCommitAs_RejectsControlChars(t *testing.T) {
	dir := setupBareInitRepo(t)
	cases := []Identity{
		{Name: "Alice\nEvil", Email: "a@b.c"},
		{Name: "Alice", Email: "a\nb@c.d"},
		{Name: "Alice\r", Email: "a@b.c"},
		{Name: "Alice\x00", Email: "a@b.c"},
	}
	for _, id := range cases {
		err := CommitAs(context.Background(), dir, id, "first", true)
		if !errors.Is(err, ErrInvalidIdentity) {
			t.Errorf("%+v: expected ErrInvalidIdentity, got %v", id, err)
		}
	}
}

func TestEnsureInitialCommit_WithIdentity_OverridesGlobal(t *testing.T) {
	dir := setupRepoWithIdentity(t) // global "T <t@e.com>"
	id := Identity{Name: "Bob Niu", Email: "bob@niuniu.local"}
	if err := EnsureInitialCommit(context.Background(), dir, id); err != nil {
		t.Fatalf("EnsureInitialCommit: %v", err)
	}
	out, _ := exec.Command("git", "-C", dir, "log", "-1", "--format=%an <%ae>").Output()
	got := strings.TrimSpace(string(out))
	if got != "Bob Niu <bob@niuniu.local>" {
		t.Fatalf("author = %q, want %q", got, "Bob Niu <bob@niuniu.local>")
	}
}

func TestEnsureInitialCommit_WithIdentity_WorksWithoutGlobal(t *testing.T) {
	dir := setupBareInitRepo(t)
	id := Identity{Name: "Carol Niu", Email: "carol@niuniu.local"}
	if err := EnsureInitialCommit(context.Background(), dir, id); err != nil {
		t.Fatalf("EnsureInitialCommit: %v", err)
	}
	out, _ := exec.Command("git", "-C", dir, "log", "-1", "--format=%an <%ae>").Output()
	got := strings.TrimSpace(string(out))
	if got != "Carol Niu <carol@niuniu.local>" {
		t.Fatalf("author = %q, want %q", got, "Carol Niu <carol@niuniu.local>")
	}
}

// mustRun runs a command in dir and fails the test on error.
func mustRun(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %s: %v", name, args, out, err)
	}
}
