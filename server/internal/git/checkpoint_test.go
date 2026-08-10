package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initCkptRepo makes a fresh repo with one baseline commit and returns its dir.
func initCkptRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	dir := t.TempDir()
	runGit(t, "", "init", "-q", "-b", "main", dir)
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "test")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	runGit(t, dir, "config", "core.autocrlf", "false")
	writeFile(t, filepath.Join(dir, "base.txt"), "base\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// TestWriteCheckpoint_HiddenRefNoBranchPollution verifies a checkpoint is created
// as refs/niuniu/... pointing at a commit, without moving HEAD or the branch, and
// that the ref never surfaces as a branch (不污染 git branch 历史).
func TestWriteCheckpoint_HiddenRefNoBranchPollution(t *testing.T) {
	dir := initCkptRepo(t)
	headBefore := revParse(t, dir, "HEAD")

	writeFile(t, filepath.Join(dir, "work.txt"), "step1\n")
	ref := CheckpointRef("42", "7", 1)
	commit, _, err := WriteCheckpoint(dir, ref, "", "step 1")
	if err != nil {
		t.Fatalf("WriteCheckpoint: %v", err)
	}
	if commit == "" {
		t.Fatal("empty commit")
	}

	// HEAD and the branch are unchanged.
	if got := revParse(t, dir, "HEAD"); got != headBefore {
		t.Errorf("HEAD moved: %s -> %s", headBefore, got)
	}
	// The ref exists and points at the snapshot commit.
	if got := ResolveRevision(dir, ref); got != commit {
		t.Errorf("ref %s = %q, want %q", ref, got, commit)
	}
	// The snapshot captured the uncommitted file.
	if content := showFile(t, dir, commit, "work.txt"); content != "step1\n" {
		t.Errorf("snapshot work.txt = %q, want step1", content)
	}
	// No branch pollution: refs/niuniu is not a branch.
	out, err := exec.Command("git", "-C", dir, "branch", "-a", "--format=%(refname)").Output()
	if err != nil {
		t.Fatalf("branch -a: %v", err)
	}
	if strings.Contains(string(out), "refs/niuniu") {
		t.Errorf("checkpoint ref leaked into branch list: %s", out)
	}
	// The real index is untouched: the working file is still uncommitted/untracked.
	st, _ := Status(dir)
	found := false
	for _, s := range st {
		if s.Path == "work.txt" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected work.txt to remain an uncommitted change after checkpoint; status=%v", st)
	}
}

// TestListCheckpoints_TimelineOrder verifies multiple steps chain into a parent
// timeline and list in ascending step order regardless of creation order.
func TestListCheckpoints_TimelineOrder(t *testing.T) {
	dir := initCkptRepo(t)
	prefix := CheckpointRefPrefix("1", "1")

	writeFile(t, filepath.Join(dir, "f.txt"), "v1\n")
	c1, _, err := WriteCheckpoint(dir, CheckpointRef("1", "1", 1), "", "step 1")
	if err != nil {
		t.Fatalf("cp1: %v", err)
	}
	writeFile(t, filepath.Join(dir, "f.txt"), "v2\n")
	c2, _, err := WriteCheckpoint(dir, CheckpointRef("1", "1", 2), c1, "step 2")
	if err != nil {
		t.Fatalf("cp2: %v", err)
	}
	writeFile(t, filepath.Join(dir, "f.txt"), "v3\n")
	c3, _, err := WriteCheckpoint(dir, CheckpointRef("1", "1", 3), c2, "step 3")
	if err != nil {
		t.Fatalf("cp3: %v", err)
	}

	cps, err := ListCheckpoints(dir, prefix)
	if err != nil {
		t.Fatalf("ListCheckpoints: %v", err)
	}
	if len(cps) != 3 {
		t.Fatalf("got %d checkpoints, want 3: %+v", len(cps), cps)
	}
	if cps[0].Step != 1 || cps[1].Step != 2 || cps[2].Step != 3 {
		t.Errorf("steps out of order: %d,%d,%d", cps[0].Step, cps[1].Step, cps[2].Step)
	}
	if cps[0].Commit != c1 || cps[2].Commit != c3 {
		t.Errorf("commit mismatch")
	}
	// Parent chain: step2's parent is step1's commit, step3's parent is step2's.
	if cps[1].Parent != c1 {
		t.Errorf("step2 parent = %q, want %q", cps[1].Parent, c1)
	}
	if cps[2].Parent != c2 {
		t.Errorf("step3 parent = %q, want %q", cps[2].Parent, c2)
	}
	if cps[1].Message != "step 2" {
		t.Errorf("step2 message = %q", cps[1].Message)
	}
	if cps[0].CreatedAt == "" {
		t.Errorf("expected a committer date")
	}
}

// TestCheckpointDiff verifies the per-step diff (vs parent) and the between-two
// diff both report the expected file changes.
func TestCheckpointDiff(t *testing.T) {
	dir := initCkptRepo(t)

	writeFile(t, filepath.Join(dir, "a.txt"), "one\n")
	c1, _, err := WriteCheckpoint(dir, CheckpointRef("1", "1", 1), "", "step 1")
	if err != nil {
		t.Fatalf("cp1: %v", err)
	}
	writeFile(t, filepath.Join(dir, "a.txt"), "one\ntwo\n")
	writeFile(t, filepath.Join(dir, "b.txt"), "new\n")
	c2, _, err := WriteCheckpoint(dir, CheckpointRef("1", "1", 2), c1, "step 2")
	if err != nil {
		t.Fatalf("cp2: %v", err)
	}

	// Step-2's own change (vs parent): a.txt modified, b.txt added.
	diffs, err := CheckpointDiff(dir, "", c2)
	if err != nil {
		t.Fatalf("CheckpointDiff parent: %v", err)
	}
	byPath := indexByPath(diffs)
	if fd, ok := byPath["b.txt"]; !ok || fd.Status != "added" {
		t.Errorf("expected b.txt added, got %v", paths(diffs))
	}
	if _, ok := byPath["a.txt"]; !ok {
		t.Errorf("expected a.txt modified, got %v", paths(diffs))
	}

	// Between step1 and step2.
	diffs2, err := CheckpointDiff(dir, c1, c2)
	if err != nil {
		t.Fatalf("CheckpointDiff range: %v", err)
	}
	if len(diffs2) != 2 {
		t.Errorf("expected 2 changed files between c1..c2, got %v", paths(diffs2))
	}
}

// TestRevertToCheckpoint_RestoresAndPreservesLaterRefs is the core safety test:
// reverting to an earlier checkpoint restores files exactly, removes files created
// afterwards, preserves ignored files, and — crucially — leaves the later checkpoint
// ref intact so no work is lost (a subsequent revert forward re-applies it).
func TestRevertToCheckpoint_RestoresAndPreservesLaterRefs(t *testing.T) {
	dir := initCkptRepo(t)

	// Ignored file that must survive a revert.
	writeFile(t, filepath.Join(dir, ".gitignore"), "ignored.txt\n")
	writeFile(t, filepath.Join(dir, "ignored.txt"), "keep-me\n")

	// Step 1: a.txt = "s1".
	writeFile(t, filepath.Join(dir, "a.txt"), "s1\n")
	c1, _, err := WriteCheckpoint(dir, CheckpointRef("1", "1", 1), "", "step 1")
	if err != nil {
		t.Fatalf("cp1: %v", err)
	}

	// Step 2: a.txt = "s2", plus a new file added.txt.
	writeFile(t, filepath.Join(dir, "a.txt"), "s2\n")
	writeFile(t, filepath.Join(dir, "added.txt"), "added-in-2\n")
	ref2 := CheckpointRef("1", "1", 2)
	c2, _, err := WriteCheckpoint(dir, ref2, c1, "step 2")
	if err != nil {
		t.Fatalf("cp2: %v", err)
	}

	// Revert to step 1.
	if err := RevertToCheckpoint(dir, c1); err != nil {
		t.Fatalf("RevertToCheckpoint c1: %v", err)
	}
	if got := readFile(t, filepath.Join(dir, "a.txt")); got != "s1\n" {
		t.Errorf("after revert a.txt = %q, want s1", got)
	}
	// added.txt (created in step 2) must be gone.
	if _, err := os.Stat(filepath.Join(dir, "added.txt")); !os.IsNotExist(err) {
		t.Errorf("added.txt should have been removed by revert to step1")
	}
	// Ignored file survives.
	if got := readFile(t, filepath.Join(dir, "ignored.txt")); got != "keep-me\n" {
		t.Errorf("ignored.txt clobbered by revert: %q", got)
	}
	// The later checkpoint ref still resolves — no work lost.
	if got := ResolveRevision(dir, ref2); got != c2 {
		t.Errorf("later checkpoint ref lost: %q, want %q", got, c2)
	}

	// Revert forward to step 2 fully re-applies it.
	if err := RevertToCheckpoint(dir, c2); err != nil {
		t.Fatalf("RevertToCheckpoint c2: %v", err)
	}
	if got := readFile(t, filepath.Join(dir, "a.txt")); got != "s2\n" {
		t.Errorf("after forward revert a.txt = %q, want s2", got)
	}
	if got := readFile(t, filepath.Join(dir, "added.txt")); got != "added-in-2\n" {
		t.Errorf("added.txt not restored on forward revert: %q", got)
	}
}

// TestDeleteCheckpoint_Idempotent verifies delete removes the ref and a second
// delete is a no-op.
func TestDeleteCheckpoint_Idempotent(t *testing.T) {
	dir := initCkptRepo(t)
	ref := CheckpointRef("1", "1", 1)
	if _, _, err := WriteCheckpoint(dir, ref, "", "step 1"); err != nil {
		t.Fatalf("cp: %v", err)
	}
	if err := DeleteCheckpoint(dir, ref); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := ResolveRevision(dir, ref); got != "" {
		t.Errorf("ref still resolves after delete: %q", got)
	}
	if err := DeleteCheckpoint(dir, ref); err != nil {
		t.Errorf("second delete should be a no-op, got %v", err)
	}
}

// showFile returns the content of a file at a commit.
func showFile(t *testing.T, dir, commit, path string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "show", commit+":"+path).Output()
	if err != nil {
		t.Fatalf("git show %s:%s: %v", commit, path, err)
	}
	return string(out)
}
