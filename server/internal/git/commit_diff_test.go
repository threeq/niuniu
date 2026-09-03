package git

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// TestCommitDiff covers what the commit-detail view needs and could not get from
// `--name-status`: the actual lines a commit changed, for an ordinary commit, a
// root commit (no parent to diff against) and a merge commit (which must report
// what it brought in rather than an empty diff, and must not list a file twice).
func TestCommitDiff(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}

	dir := t.TempDir()
	runGit(t, "", "init", "-q", "-b", "main", dir)
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "test")
	runGit(t, dir, "config", "commit.gpgsign", "false")

	// Root commit — no parent.
	writeFile(t, filepath.Join(dir, "a.txt"), "one\ntwo\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "root")
	root := revParse(t, dir, "HEAD")

	// Ordinary commit: one modified line, one new file.
	writeFile(t, filepath.Join(dir, "a.txt"), "one\nTWO\n")
	writeFile(t, filepath.Join(dir, "b.txt"), "bee\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "edit")
	edit := revParse(t, dir, "HEAD")

	t.Run("root commit reports its whole tree as additions", func(t *testing.T) {
		diffs, err := CommitDiff(dir, root)
		if err != nil {
			t.Fatalf("CommitDiff: %v", err)
		}
		byPath := indexByPath(diffs)
		fd, ok := byPath["a.txt"]
		if !ok {
			t.Fatalf("expected a.txt, got %v", paths(diffs))
		}
		if fd.Status != "added" {
			t.Errorf("root commit file status = %q, want added", fd.Status)
		}
		if fd.Additions != 2 {
			t.Errorf("additions = %d, want 2", fd.Additions)
		}
	})

	t.Run("ordinary commit carries structured hunks, not just names", func(t *testing.T) {
		diffs, err := CommitDiff(dir, edit)
		if err != nil {
			t.Fatalf("CommitDiff: %v", err)
		}
		byPath := indexByPath(diffs)
		fd, ok := byPath["a.txt"]
		if !ok {
			t.Fatalf("expected a.txt, got %v", paths(diffs))
		}
		if len(fd.Hunks) == 0 {
			t.Fatal("a.txt has no hunks — the whole point of this endpoint is line-level detail")
		}
		var added, deleted []string
		for _, h := range fd.Hunks {
			for _, l := range h.Lines {
				switch l.Type {
				case "add":
					added = append(added, l.Content)
				case "delete":
					deleted = append(deleted, l.Content)
				}
			}
		}
		if len(added) != 1 || added[0] != "TWO" {
			t.Errorf("added lines = %v, want [TWO]", added)
		}
		if len(deleted) != 1 || deleted[0] != "two" {
			t.Errorf("deleted lines = %v, want [two]", deleted)
		}
		if _, ok := byPath["b.txt"]; !ok {
			t.Errorf("expected b.txt in the same commit, got %v", paths(diffs))
		}
	})

	// Merge commit: a side branch touching a different file, merged with --no-ff.
	runGit(t, dir, "checkout", "-q", "-b", "side", root)
	writeFile(t, filepath.Join(dir, "c.txt"), "sea\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "side work")
	runGit(t, dir, "checkout", "-q", "main")
	runGit(t, dir, "merge", "-q", "--no-ff", "--no-edit", "side")
	merge := revParse(t, dir, "HEAD")

	t.Run("merge commit reports what it brought in, each file once", func(t *testing.T) {
		diffs, err := CommitDiff(dir, merge)
		if err != nil {
			t.Fatalf("CommitDiff: %v", err)
		}
		// Against the first parent (main), the merge introduces c.txt.
		byPath := indexByPath(diffs)
		if _, ok := byPath["c.txt"]; !ok {
			t.Fatalf("expected c.txt from the merged branch, got %v", paths(diffs))
		}
		// `git show -m` would emit one section per parent; parseDiff would then
		// return the same path twice and the viewer would render duplicate files.
		seen := map[string]int{}
		for _, d := range diffs {
			seen[d.Path]++
		}
		for p, n := range seen {
			if n > 1 {
				t.Errorf("path %q appears %d times — merge diff must not be per-parent", p, n)
			}
		}
	})

	t.Run("unknown revision is an error, not an empty diff", func(t *testing.T) {
		if _, err := CommitDiff(dir, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"); err == nil {
			t.Error("expected an error for a nonexistent commit")
		}
	})
}
