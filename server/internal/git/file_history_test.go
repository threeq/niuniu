package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initFileHistoryRepo builds a small repo whose history contains a rename, so
// --follow has something to walk past.
func initFileHistoryRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Tester", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=Tester", "GIT_COMMITTER_EMAIL=t@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v (%s)", strings.Join(args, " "), err, out)
		}
	}

	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run("init", "-q")
	run("config", "user.name", "Tester")
	run("config", "user.email", "t@example.com")

	write("old-name.txt", "line one\n")
	run("add", ".")
	run("commit", "-q", "-m", "first: add old-name")

	run("mv", "old-name.txt", "new-name.txt")
	run("commit", "-q", "-m", "second: rename to new-name")

	write("new-name.txt", "line one\nline two\n")
	run("add", ".")
	run("commit", "-q", "-m", "third: append line two")

	// An unrelated file: history for new-name.txt must not pick it up.
	write("other.txt", "unrelated\n")
	run("add", ".")
	run("commit", "-q", "-m", "fourth: unrelated file")

	return dir
}

func TestFileLogFollowsRenames(t *testing.T) {
	repo := initFileHistoryRepo(t)

	entries, err := FileLog(repo, "new-name.txt", 0)
	if err != nil {
		t.Fatalf("FileLog: %v", err)
	}

	// Three commits touched this file (add, rename, append); the unrelated
	// fourth must not appear.
	if len(entries) != 3 {
		var msgs []string
		for _, e := range entries {
			msgs = append(msgs, e.Message)
		}
		t.Fatalf("want 3 commits, got %d: %v", len(entries), msgs)
	}

	if !strings.HasPrefix(entries[0].Message, "third") {
		t.Errorf("newest commit = %q, want third", entries[0].Message)
	}
	if !strings.HasPrefix(entries[2].Message, "first") {
		t.Errorf("oldest commit = %q, want first", entries[2].Message)
	}

	// This is the whole reason PathAtCommit exists: at the oldest commit the
	// file was called old-name.txt, and asking for new-name.txt there would
	// yield a silently empty diff.
	if got := entries[2].PathAtCommit; got != "old-name.txt" {
		t.Errorf("PathAtCommit at oldest commit = %q, want old-name.txt", got)
	}

	for _, e := range entries {
		if e.Hash == "" || e.Author == "" || e.Date == "" {
			t.Errorf("incomplete entry: %+v", e)
		}
	}
}

func TestFileLogHonoursLimit(t *testing.T) {
	repo := initFileHistoryRepo(t)

	entries, err := FileLog(repo, "new-name.txt", 2)
	if err != nil {
		t.Fatalf("FileLog: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("limit=2 returned %d entries", len(entries))
	}
}

// A traversal path must be REFUSED, not clamped. Clamping "../other.txt" to
// "other.txt" would return a different file's history without saying so.
func TestFileLogRejectsTraversal(t *testing.T) {
	repo := initFileHistoryRepo(t)

	for _, bad := range []string{"../other.txt", "../../etc/passwd", "a/../../b", "", "."} {
		if _, err := FileLog(repo, bad, 0); err == nil {
			t.Errorf("FileLog(%q) succeeded, want rejection", bad)
		}
	}
}

func TestFileDiffAtCommitUsesHistoricalName(t *testing.T) {
	repo := initFileHistoryRepo(t)

	entries, err := FileLog(repo, "new-name.txt", 0)
	if err != nil {
		t.Fatalf("FileLog: %v", err)
	}

	patch, err := FileDiffAtCommit(repo, entries[0].Hash, entries[0].PathAtCommit)
	if err != nil {
		t.Fatalf("FileDiffAtCommit: %v", err)
	}
	if !strings.Contains(patch, "+line two") {
		t.Errorf("patch missing added line:\n%s", patch)
	}

	// The oldest commit only resolves under its historical name.
	oldest := entries[2]
	patchOld, err := FileDiffAtCommit(repo, oldest.Hash, oldest.PathAtCommit)
	if err != nil {
		t.Fatalf("FileDiffAtCommit(oldest): %v", err)
	}
	if !strings.Contains(patchOld, "+line one") {
		t.Errorf("oldest patch missing content:\n%s", patchOld)
	}
}

func TestFileDiffAtCommitRejectsBadCommitish(t *testing.T) {
	repo := initFileHistoryRepo(t)

	for _, bad := range []string{"--output=/tmp/pwned", "-x", "a;rm -rf /", "$(whoami)", ""} {
		if _, err := FileDiffAtCommit(repo, bad, "new-name.txt"); err == nil {
			t.Errorf("FileDiffAtCommit(%q) succeeded, want rejection", bad)
		}
	}
}
