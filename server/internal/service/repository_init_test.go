package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/git"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// newRepoServiceForInitTest returns a RepositoryService backed by an in-memory
// SQLite, ready for finishCreate calls. It also redirects --global git config
// to a temp file unique per t so identity tests don't pollute the real config.
func newRepoServiceForInitTest(t *testing.T) *RepositoryService {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), ".gitconfig")
	if err := os.WriteFile(cfg, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	rawDB := openWorkspaceTestDB(t)
	q := store.New(rawDB)
	return NewRepositoryService(q, rawDB, nil, "")
}

func TestFinishCreate_AutoInitWithoutIdentity(t *testing.T) {
	s := newRepoServiceForInitTest(t)
	dir := t.TempDir()
	_, _, err := s.finishCreate(context.Background(), CreateRepositoryInput{
		Name:      "no-id",
		Path:      dir,
		AutoInit:  true,
		OwnerType: "user",
		OwnerID:   1,
	})
	if err == nil || !strings.Contains(err.Error(), "GIT_IDENTITY_MISSING") {
		t.Fatalf("expected GIT_IDENTITY_MISSING, got %v", err)
	}
	// And we did NOT create a half-initialised .git
	if _, err := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
		t.Errorf(".git should not exist when identity check fails first")
	}
}

func TestFinishCreate_AutoInitEmptyDir(t *testing.T) {
	s := newRepoServiceForInitTest(t)
	if err := git.SetGlobalIdentity(context.Background(), "T", "t@e.com"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	repo, _, err := s.finishCreate(context.Background(), CreateRepositoryInput{
		Name:      "empty-init",
		Path:      dir,
		AutoInit:  true,
		OwnerType: "user",
		OwnerID:   1,
	})
	if err != nil {
		t.Fatalf("finishCreate: %v", err)
	}
	if repo.ID == 0 {
		t.Fatal("repo not persisted")
	}
	// Since #233, auto-init always writes a default .gitignore, so the working
	// tree is never truly empty: the placeholder .keep-niuniu is unnecessary and
	// .gitignore carries the first commit instead.
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); err != nil {
		t.Errorf(".gitignore missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".keep-niuniu")); !os.IsNotExist(err) {
		t.Error(".keep-niuniu should NOT be created once .gitignore makes the tree non-empty")
	}
	// HEAD exists
	if exec.Command("git", "-C", dir, "rev-parse", "--verify", "HEAD").Run() != nil {
		t.Error("HEAD not set after init")
	}
}

func TestFinishCreate_AutoInitWithExistingFiles(t *testing.T) {
	s := newRepoServiceForInitTest(t)
	if err := git.SetGlobalIdentity(context.Background(), "T", "t@e.com"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.finishCreate(context.Background(), CreateRepositoryInput{
		Name:      "with-files",
		Path:      dir,
		AutoInit:  true,
		OwnerType: "user",
		OwnerID:   1,
	}); err != nil {
		t.Fatalf("finishCreate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".keep-niuniu")); !os.IsNotExist(err) {
		t.Error(".keep-niuniu should NOT be created when dir has files")
	}
	out, _ := exec.Command("git", "-C", dir, "log", "-1", "--name-only", "--format=").Output()
	if !strings.Contains(string(out), "README.md") {
		t.Errorf("README.md not in first commit: %s", out)
	}
}

// TestFinishCreate_AutoInitEnablesLFS verifies issue #233: a freshly auto-init'd
// repo gets a default .gitignore plus Git LFS tracking for media, both captured
// in the first commit, and a large media file is stored via LFS (not bloating
// the tree). Skips when git-lfs is not installed on the host.
func TestFinishCreate_AutoInitEnablesLFS(t *testing.T) {
	if !git.LFSAvailable(context.Background()) {
		t.Skip("git-lfs not installed; skipping LFS coverage")
	}
	s := newRepoServiceForInitTest(t)
	if err := git.SetGlobalIdentity(context.Background(), "T", "t@e.com"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// A media file that should be tracked by LFS once enabled.
	if err := os.WriteFile(filepath.Join(dir, "clip.mp4"), []byte("fake-mp4-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, warnings, err := s.finishCreate(context.Background(), CreateRepositoryInput{
		Name:      "lfs-init",
		Path:      dir,
		AutoInit:  true,
		OwnerType: "user",
		OwnerID:   1,
	})
	if err != nil {
		t.Fatalf("finishCreate: %v", err)
	}
	for _, w := range warnings {
		if w == WarnGitLFSMissing {
			t.Fatalf("unexpected WARN_GIT_LFS_MISSING when git-lfs is available")
		}
	}
	// .gitignore and .gitattributes exist and are committed.
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); err != nil {
		t.Errorf(".gitignore missing: %v", err)
	}
	attrs, err := os.ReadFile(filepath.Join(dir, ".gitattributes"))
	if err != nil {
		t.Fatalf(".gitattributes missing: %v", err)
	}
	if !strings.Contains(string(attrs), "*.mp4") || !strings.Contains(string(attrs), "filter=lfs") {
		t.Errorf(".gitattributes missing mp4 LFS rule: %s", attrs)
	}
	committed, _ := exec.Command("git", "-C", dir, "log", "-1", "--name-only", "--format=").Output()
	for _, want := range []string{".gitignore", ".gitattributes", "clip.mp4"} {
		if !strings.Contains(string(committed), want) {
			t.Errorf("%s not in first commit: %s", want, committed)
		}
	}
	// The mp4 is tracked by LFS.
	lsFiles, _ := exec.Command("git", "-C", dir, "lfs", "ls-files").Output()
	if !strings.Contains(string(lsFiles), "clip.mp4") {
		t.Errorf("clip.mp4 not tracked by LFS: %s", lsFiles)
	}
}
