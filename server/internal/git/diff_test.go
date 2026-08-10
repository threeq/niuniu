package git

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// TestDiff_VsBaseline exercises the "vs baseline" semantics added for the diff
// viewer: base...HEAD (merge-base → working tree) includes both committed and
// uncommitted changes on the branch side, excludes commits the base made after
// divergence, and excludes untracked files; an empty base falls back to
// "git diff HEAD" (uncommitted only).
func TestDiff_VsBaseline(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}

	dir := t.TempDir()
	runGit(t, "", "init", "-q", "-b", "main", dir)
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "test")
	runGit(t, dir, "config", "commit.gpgsign", "false")

	// Baseline commit on main.
	writeFile(t, filepath.Join(dir, "file1.txt"), "orig\n")
	writeFile(t, filepath.Join(dir, "keep.txt"), "keep\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "init")

	// Feature branch: a committed change + a committed new file.
	runGit(t, dir, "checkout", "-q", "-b", "feat")
	writeFile(t, filepath.Join(dir, "file1.txt"), "orig\nfeat-committed\n")
	writeFile(t, filepath.Join(dir, "added.txt"), "new file\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "feat work")

	// main advances after the branch point — must NOT show up in feat's vs-base diff.
	runGit(t, dir, "checkout", "-q", "main")
	writeFile(t, filepath.Join(dir, "keep.txt"), "keep\nmain-only\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "main moves")
	runGit(t, dir, "checkout", "-q", "feat")

	// Uncommitted edit to a tracked file + an untracked file.
	writeFile(t, filepath.Join(dir, "file1.txt"), "orig\nfeat-committed\nuncommitted\n")
	writeFile(t, filepath.Join(dir, "untracked.txt"), "untracked\n")

	t.Run("base...HEAD includes committed+uncommitted, excludes base-only and untracked", func(t *testing.T) {
		diffs, err := Diff(dir, "main")
		if err != nil {
			t.Fatalf("Diff: %v", err)
		}
		byPath := indexByPath(diffs)
		if _, ok := byPath["file1.txt"]; !ok {
			t.Errorf("expected file1.txt in diff, got %v", paths(diffs))
		}
		if fd, ok := byPath["added.txt"]; !ok {
			t.Errorf("expected added.txt in diff, got %v", paths(diffs))
		} else if fd.Status != "added" {
			t.Errorf("added.txt status = %q, want added", fd.Status)
		}
		if _, ok := byPath["keep.txt"]; ok {
			t.Errorf("keep.txt (base-only commit) must not appear in base...HEAD diff")
		}
		if _, ok := byPath["untracked.txt"]; ok {
			t.Errorf("untracked.txt must not appear in tracked diff")
		}
		// Both the committed and uncommitted added lines are counted.
		if fd := byPath["file1.txt"]; fd.Additions != 2 {
			t.Errorf("file1.txt additions = %d, want 2 (committed+uncommitted)", fd.Additions)
		}
	})

	t.Run("empty base falls back to git diff HEAD (uncommitted only)", func(t *testing.T) {
		diffs, err := Diff(dir, "")
		if err != nil {
			t.Fatalf("Diff: %v", err)
		}
		byPath := indexByPath(diffs)
		if fd, ok := byPath["file1.txt"]; !ok {
			t.Errorf("expected file1.txt (uncommitted), got %v", paths(diffs))
		} else if fd.Additions != 1 {
			t.Errorf("file1.txt additions = %d, want 1 (uncommitted only)", fd.Additions)
		}
		if _, ok := byPath["added.txt"]; ok {
			t.Errorf("added.txt is committed; must not appear in HEAD fallback diff")
		}
	})

	t.Run("UntrackedDiffs lists untracked files as added", func(t *testing.T) {
		diffs, err := UntrackedDiffs(dir)
		if err != nil {
			t.Fatalf("UntrackedDiffs: %v", err)
		}
		byPath := indexByPath(diffs)
		fd, ok := byPath["untracked.txt"]
		if !ok {
			t.Fatalf("expected untracked.txt, got %v", paths(diffs))
		}
		if fd.Status != "added" {
			t.Errorf("untracked.txt status = %q, want added", fd.Status)
		}
		if len(byPath) != 1 {
			t.Errorf("expected exactly the untracked file, got %v", paths(diffs))
		}
	})

	t.Run("DiffFile resolves tracked-vs-base, untracked, and missing", func(t *testing.T) {
		if fd, err := DiffFile(dir, "main", "added.txt"); err != nil {
			t.Errorf("DiffFile added.txt: %v", err)
		} else if fd.Status != "added" {
			t.Errorf("added.txt status = %q, want added", fd.Status)
		}
		if fd, err := DiffFile(dir, "main", "untracked.txt"); err != nil {
			t.Errorf("DiffFile untracked.txt: %v", err)
		} else if fd.Status != "added" {
			t.Errorf("untracked.txt status = %q, want added", fd.Status)
		}
		// keep.txt is unchanged on feat vs base and not untracked → no diff.
		if _, err := DiffFile(dir, "main", "keep.txt"); err == nil {
			t.Errorf("DiffFile keep.txt: expected error for unchanged file")
		}
	})
}

func indexByPath(diffs []FileDiff) map[string]FileDiff {
	m := make(map[string]FileDiff, len(diffs))
	for _, d := range diffs {
		m[d.Path] = d
	}
	return m
}

func paths(diffs []FileDiff) []string {
	out := make([]string, 0, len(diffs))
	for _, d := range diffs {
		out = append(out, d.Path)
	}
	return out
}
