package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorktreeAdd_BaseBranch verifies that the optional baseBranch argument
// causes the new worktree's branch to be forked from that ref rather than HEAD.
// Regression: the workspace-creation flow used to ignore the user-selected
// "原分支" (source branch) because WorktreeAdd never passed it through to git.
func TestWorktreeAdd_BaseBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}

	root := t.TempDir()
	repo := filepath.Join(root, "repo")

	mainSHA, developSHA := setupTestRepo(t, repo)

	tests := []struct {
		name       string
		newBranch  string
		baseBranch string
		wantTipSHA string
	}{
		{
			name:       "empty baseBranch forks from HEAD",
			newBranch:  "feat/from-head",
			baseBranch: "",
			wantTipSHA: mainSHA,
		},
		{
			name:       "explicit baseBranch forks from that ref",
			newBranch:  "feat/from-develop",
			baseBranch: "develop",
			wantTipSHA: developSHA,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wt := filepath.Join(root, "wt-"+strings.ReplaceAll(tc.newBranch, "/", "_"))
			if err := WorktreeAdd(repo, wt, tc.newBranch, tc.baseBranch); err != nil {
				t.Fatalf("WorktreeAdd: %v", err)
			}
			t.Cleanup(func() { _ = WorktreeRemove(repo, wt) })

			gotSHA := revParse(t, wt, "HEAD")
			if gotSHA != tc.wantTipSHA {
				t.Fatalf("worktree HEAD = %s, want %s (baseBranch=%q)",
					gotSHA, tc.wantTipSHA, tc.baseBranch)
			}

			gotBranch := revParse(t, wt, "--abbrev-ref HEAD")
			if gotBranch != tc.newBranch {
				t.Fatalf("worktree branch = %s, want %s", gotBranch, tc.newBranch)
			}
		})
	}
}

// TestMergeFastForwardOnly verifies the sync helper used to pull the epic feature
// branch into the epic's own workspace: it fast-forwards when the current branch is
// strictly behind, and on divergence it refuses WITHOUT leaving the worktree in a
// MERGING/conflicted state (the safety guarantee the Epic engine relies on).
func TestMergeFastForwardOnly(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}

	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	mainSHA, developSHA := setupTestRepo(t, repo) // main→A ; develop→A,B,C (develop is ahead of main)

	t.Run("fast-forwards when behind", func(t *testing.T) {
		wt := filepath.Join(root, "wt-ff")
		// New branch off main (behind develop); develop is a strict descendant.
		runGit(t, repo, "branch", "feat-ff", "main")
		if err := WorktreeAdd(repo, wt, "wt-feat-ff", "feat-ff"); err != nil {
			t.Fatalf("WorktreeAdd: %v", err)
		}
		t.Cleanup(func() { _ = WorktreeRemove(repo, wt) })

		if err := MergeFastForwardOnly(wt, "develop"); err != nil {
			t.Fatalf("MergeFastForwardOnly (ff case): %v", err)
		}
		if got := revParse(t, wt, "HEAD"); got != developSHA {
			t.Fatalf("after ff, HEAD = %s, want develop tip %s", got, developSHA)
		}
	})

	t.Run("refuses cleanly on divergence", func(t *testing.T) {
		wt := filepath.Join(root, "wt-div")
		runGit(t, repo, "branch", "feat-div", "main")
		if err := WorktreeAdd(repo, wt, "wt-feat-div", "feat-div"); err != nil {
			t.Fatalf("WorktreeAdd: %v", err)
		}
		t.Cleanup(func() { _ = WorktreeRemove(repo, wt) })

		// Diverge: commit on the worktree branch so develop is no longer an ancestor.
		writeFile(t, filepath.Join(wt, "local"), "local")
		runGit(t, wt, "add", "local")
		runGit(t, wt, "commit", "-q", "-m", "local divergent")
		divergedSHA := revParse(t, wt, "HEAD")

		if err := MergeFastForwardOnly(wt, "develop"); err == nil {
			t.Fatal("MergeFastForwardOnly should refuse a non-fast-forward merge")
		}
		// The branch tip must be unchanged and the worktree must NOT be mid-merge.
		if got := revParse(t, wt, "HEAD"); got != divergedSHA {
			t.Fatalf("HEAD moved on a refused ff: got %s, want %s", got, divergedSHA)
		}
		if _, err := os.Stat(filepath.Join(repo, ".git", "worktrees")); err == nil {
			// MERGE_HEAD lives in the per-worktree git dir; assert no merge in progress
			// by checking `git merge --abort` has nothing to abort.
			out, _ := exec.Command("git", "-C", wt, "rev-parse", "--verify", "-q", "MERGE_HEAD").CombinedOutput()
			if strings.TrimSpace(string(out)) != "" {
				t.Fatalf("worktree left in MERGING state after refused ff: MERGE_HEAD=%s", out)
			}
		}
		_ = mainSHA
	})
}

// TestWorktreeAddCheckoutExisting pins the Epic unified-branch helper (spec
// 2026-09-28 §1): the epic control workspace's worktree must check out the
// ALREADY-EXISTING epic/<id> branch as-is (no -b), so the workspace sits
// directly on the branch children merge into.
func TestWorktreeAddCheckoutExisting(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}

	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	mainSHA, developSHA := setupTestRepo(t, repo)

	// First worktree checks out develop and stays alive across both subtests
	// (parent-level cleanup), so the duplicate-checkout guard is exercisable.
	wtExisting := filepath.Join(root, "wt-existing")
	if err := WorktreeAddCheckoutExisting(repo, wtExisting, "develop"); err != nil {
		t.Fatalf("WorktreeAddCheckoutExisting: %v", err)
	}
	t.Cleanup(func() { _ = WorktreeRemove(repo, wtExisting) })

	t.Run("checks out the existing branch as-is", func(t *testing.T) {
		if got := revParse(t, wtExisting, "--abbrev-ref HEAD"); got != "develop" {
			t.Fatalf("worktree branch = %s, want develop (checked out, not forked)", got)
		}
		if got := revParse(t, wtExisting, "HEAD"); got != developSHA {
			t.Fatalf("worktree HEAD = %s, want develop tip %s", got, developSHA)
		}
		// No new branch may be created — the set of branches is unchanged.
		out, err := exec.Command("git", "-C", repo, "for-each-ref", "--format=%(refname:short)", "refs/heads").Output()
		if err != nil {
			t.Fatalf("for-each-ref: %v", err)
		}
		for _, b := range strings.Split(string(out), "\n") {
			if strings.TrimSpace(b) == "wt-existing" {
				t.Fatal("WorktreeAddCheckoutExisting created a new branch; must check out the existing one")
			}
		}
		_ = mainSHA
	})

	t.Run("refuses when the branch is already checked out elsewhere", func(t *testing.T) {
		// develop is still checked out in the first worktree; a second
		// checkout must fail with git's native guard error.
		wt2 := filepath.Join(root, "wt-dup")
		err := WorktreeAddCheckoutExisting(repo, wt2, "develop")
		if err == nil {
			t.Fatal("expected an error checking out a branch already checked out in another worktree")
		}
	})
}

// setupTestRepo initializes a repo with main → A and develop → A,B,C, returning the
// tip SHAs of main and develop respectively.
func setupTestRepo(t *testing.T, dir string) (mainSHA, developSHA string) {
	t.Helper()
	runGit(t, "", "init", "-q", "-b", "main", dir)
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "test")
	runGit(t, dir, "config", "commit.gpgsign", "false")

	writeFile(t, filepath.Join(dir, "a"), "a")
	runGit(t, dir, "add", "a")
	runGit(t, dir, "commit", "-q", "-m", "a")
	mainSHA = revParse(t, dir, "main")

	runGit(t, dir, "checkout", "-q", "-b", "develop")
	writeFile(t, filepath.Join(dir, "b"), "b")
	runGit(t, dir, "add", "b")
	runGit(t, dir, "commit", "-q", "-m", "b")
	writeFile(t, filepath.Join(dir, "c"), "c")
	runGit(t, dir, "add", "c")
	runGit(t, dir, "commit", "-q", "-m", "c")
	developSHA = revParse(t, dir, "develop")

	runGit(t, dir, "checkout", "-q", "main")
	return mainSHA, developSHA
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	var cmd *exec.Cmd
	if dir == "" {
		cmd = exec.Command("git", args...)
	} else {
		cmd = exec.Command("git", append([]string{"-C", dir}, args...)...)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func revParse(t *testing.T, dir, ref string) string {
	t.Helper()
	args := append([]string{"-C", dir, "rev-parse"}, strings.Fields(ref)...)
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		t.Fatalf("git rev-parse %s: %v", ref, err)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
