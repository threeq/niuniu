package git

import (
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
)

// setupConflictRepo builds a repo where main and feature both edit the same
// file after diverging, so merging feature into main conflicts.
func setupConflictRepo(t *testing.T, dir string) {
	t.Helper()
	runGit(t, "", "init", "-q", "-b", "main", dir)
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "test")
	runGit(t, dir, "config", "commit.gpgsign", "false")

	writeFile(t, filepath.Join(dir, "shared.txt"), "base\n")
	runGit(t, dir, "add", "shared.txt")
	runGit(t, dir, "commit", "-q", "-m", "base")

	runGit(t, dir, "checkout", "-q", "-b", "feature")
	writeFile(t, filepath.Join(dir, "shared.txt"), "feature\n")
	runGit(t, dir, "add", "shared.txt")
	runGit(t, dir, "commit", "-q", "-m", "feature change")

	runGit(t, dir, "checkout", "-q", "main")
	writeFile(t, filepath.Join(dir, "shared.txt"), "main\n")
	runGit(t, dir, "add", "shared.txt")
	runGit(t, dir, "commit", "-q", "-m", "main change")
}

// TestMergeAsConflictReturnsTypedErrorWithFileList pins the Wave-1 git-layer
// contract (spec 2026-09-28 §1): a conflicted MergeAs returns a
// *MergeConflictError carrying the conflicted file list (parsed from the
// merge-tree --write-tree output), while Error() keeps rendering the same
// "conflicts detected" message callers displayed before the typed error
// existed. Neither branch may move.
func TestMergeAsConflictReturnsTypedErrorWithFileList(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}

	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	setupConflictRepo(t, repo)
	mainBefore := revParse(t, repo, "main")
	featBefore := revParse(t, repo, "feature")

	err := MergeAs(repo, "feature", "main", Identity{Name: "t", Email: "t@example.com"})
	if err == nil {
		t.Fatal("expected MergeAs to fail on a conflicting merge")
	}

	var conflict *MergeConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected *MergeConflictError via errors.As, got %T: %v", err, err)
	}
	if len(conflict.Files) == 0 {
		t.Fatal("MergeConflictError.Files is empty; want at least the conflicted path")
	}
	found := false
	for _, f := range conflict.Files {
		if f == "shared.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Files = %v, want it to contain shared.txt", conflict.Files)
	}

	// The old message shape is preserved so existing logs / error surfaces
	// render exactly as before.
	want := "merge feature into main: conflicts detected:"
	if got := conflict.Error(); len(got) < len(want) || got[:len(want)] != want {
		t.Fatalf("Error() = %q, want prefix %q", got, want)
	}

	// A conflicted merge must leave both refs untouched.
	if got := revParse(t, repo, "main"); got != mainBefore {
		t.Fatalf("main moved on conflict: %s -> %s", mainBefore, got)
	}
	if got := revParse(t, repo, "feature"); got != featBefore {
		t.Fatalf("feature moved on conflict: %s -> %s", featBefore, got)
	}
}
