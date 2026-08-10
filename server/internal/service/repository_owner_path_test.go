package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	_ "modernc.org/sqlite"
)

// seedRepoSvc spins up an in-memory DB with one user and returns a wired
// RepositoryService alongside the user's id and the dataDir root used for
// OwnerRef path computation. Shared by the relocation tests below.
func seedRepoSvc(t *testing.T) (svc *RepositoryService, userID int64, dataDir string) {
	t.Helper()
	rawDB := openWorkspaceTestDB(t)
	q := store.New(rawDB)

	if _, err := rawDB.ExecContext(context.Background(),
		`INSERT INTO users (username, password_hash, role) VALUES ('alice', 'x', 'admin')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := rawDB.QueryRowContext(context.Background(),
		`SELECT id FROM users WHERE username = 'alice'`).Scan(&userID); err != nil {
		t.Fatalf("select user: %v", err)
	}

	dataDir = t.TempDir()
	svc = NewRepositoryService(q, rawDB, nil, dataDir)
	return svc, userID, dataDir
}

// TestRepositoryAutoInitRelocatesToOwnerPath guards against the regression
// where a team-edition repo created via auto-init was stored at the
// user-supplied path (e.g. /data/git-repos/test) instead of the OwnerRef-scoped
// path under <dataDir>/{users|orgs}/<id>/repositories/<repoID>. On the team
// Docker deploy this leaked outside the persistent /data/.niuniu volume and
// was wiped on every container recreate, leaving worktrees with stale .git
// pointers and producing 404s on /api/workspaces/:id/tree/worktrees/:name.
func TestRepositoryAutoInitRelocatesToOwnerPath(t *testing.T) {
	svc, userID, dataDir := seedRepoSvc(t)

	// Caller-supplied path that lives OUTSIDE dataDir AND does not yet exist —
	// the legacy code path that triggered the bug. AutoInit creates the dir +
	// git init, then the relocation should move it under dataDir.
	userTypedPath := filepath.Join(t.TempDir(), "outside-data", "myrepo")

	repo, _, err := svc.Create(context.Background(), CreateRepositoryInput{
		Path:      userTypedPath,
		Name:      "myrepo",
		AutoInit:  true,
		OwnerType: "user",
		OwnerID:   userID,
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	wantPath := OwnerRef{Type: "user", ID: userID}.RepositoryPath(dataDir, repo.ID)
	if repo.Path != wantPath {
		t.Errorf("repo.Path = %q; want %q (OwnerRef-scoped, under dataDir)", repo.Path, wantPath)
	}

	// Disk state assertions: the original user-typed path must be gone, and
	// the relocated path must exist as a real directory.
	if _, err := os.Stat(userTypedPath); !os.IsNotExist(err) {
		t.Errorf("user-typed path should be gone after relocation; stat err = %v", err)
	}
	if info, err := os.Stat(repo.Path); err != nil {
		t.Errorf("relocated path missing on disk: %v", err)
	} else if !info.IsDir() {
		t.Errorf("relocated path is not a directory")
	}
}

// TestRepositoryAddPathDoesNotRelocate verifies that the "Add Path" flow —
// AutoInit=false on a pre-existing user-owned git repo — leaves the directory
// in place. This is the non-regression complement to the test above: a future
// "always relocate, simpler" refactor would silently move user data.
func TestRepositoryAddPathDoesNotRelocate(t *testing.T) {
	svc, userID, _ := seedRepoSvc(t)

	// Pre-create a real git repo at a user-owned location outside dataDir.
	userOwnedPath := filepath.Join(t.TempDir(), "user-projects", "existing-repo")
	if err := os.MkdirAll(userOwnedPath, 0755); err != nil {
		t.Fatalf("mkdir user repo: %v", err)
	}
	if err := exec.Command("git", "init", userOwnedPath).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	repo, _, err := svc.Create(context.Background(), CreateRepositoryInput{
		Path:      userOwnedPath,
		Name:      "existing-repo",
		AutoInit:  false,
		OwnerType: "user",
		OwnerID:   userID,
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	if repo.Path != userOwnedPath {
		t.Errorf("repo.Path = %q; want %q (Add Path must not relocate)", repo.Path, userOwnedPath)
	}
	if _, err := os.Stat(userOwnedPath); err != nil {
		t.Errorf("user-owned path should still exist after Add Path: %v", err)
	}
}

// TestRepositoryAutoInitOnExistingDirDoesNotRelocate verifies the subtler case:
// AutoInit=true with a path that ALREADY exists is treated like Add Path — the
// pre-existing directory is user-owned and must not be silently moved. This
// hardens against the personal-edition footgun where a user re-registers an
// existing project dir with the API's default auto_init=true.
func TestRepositoryAutoInitOnExistingDirDoesNotRelocate(t *testing.T) {
	svc, userID, _ := seedRepoSvc(t)

	// Pre-existing dir, NOT yet a git repo. AutoInit will git-init it but must
	// not relocate, because we didn't create the dir ourselves.
	userOwnedPath := filepath.Join(t.TempDir(), "preexisting", "projectX")
	if err := os.MkdirAll(userOwnedPath, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	repo, _, err := svc.Create(context.Background(), CreateRepositoryInput{
		Path:      userOwnedPath,
		Name:      "projectX",
		AutoInit:  true,
		OwnerType: "user",
		OwnerID:   userID,
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	if repo.Path != userOwnedPath {
		t.Errorf("repo.Path = %q; want %q (pre-existing dir must not relocate)", repo.Path, userOwnedPath)
	}
	if _, err := os.Stat(userOwnedPath); err != nil {
		t.Errorf("pre-existing path should still exist: %v", err)
	}
}
