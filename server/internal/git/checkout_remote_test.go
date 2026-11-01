package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pushNewBranch creates branch on the "other" clone, commits a file and pushes
// it, leaving the primary clone unaware of it locally.
func pushNewBranch(t *testing.T, other, branch, file, content string) {
	t.Helper()
	runGit(t, other, "checkout", "-q", "-b", branch)
	writeFile(t, filepath.Join(other, file), content)
	runGit(t, other, "add", file)
	runGit(t, other, "commit", "-q", "-m", branch)
	runGit(t, other, "push", "-q", "origin", branch)
}

// TestCheckoutRemoteBranch_CreatesTrackingBranch: a remote-only branch becomes
// a current local branch tracking it — the repository page's "checkout to
// local" action.
func TestCheckoutRemoteBranch_CreatesTrackingBranch(t *testing.T) {
	_, repo, other := setupRemotePair(t)
	pushNewBranch(t, other, "feat/x", "g.txt", "g\n")

	local, err := CheckoutRemoteBranchEnv(repo, "origin/feat/x", nil)
	if err != nil {
		t.Fatalf("CheckoutRemoteBranchEnv: %v", err)
	}
	if local != "feat/x" {
		t.Fatalf("local = %q, want feat/x", local)
	}
	if cur, err := CurrentBranch(repo); err != nil || cur != "feat/x" {
		t.Fatalf("current branch = %q (err=%v), want feat/x", cur, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "g.txt")); err != nil {
		t.Fatalf("checked-out branch content missing: %v", err)
	}
}

// TestCheckoutRemoteBranch_ExistingLocalFastForwards: when the local branch
// already exists and is behind, the action switches to it and fast-forwards —
// like a pull, but never a destructive merge.
func TestCheckoutRemoteBranch_ExistingLocalFastForwards(t *testing.T) {
	_, repo, other := setupRemotePair(t)
	pushNewBranch(t, other, "feat/y", "h.txt", "h1\n")

	// Track the branch locally first (older state), then move the remote ahead.
	runGit(t, repo, "fetch", "-q", "origin")
	runGit(t, repo, "checkout", "-q", "-b", "feat/y", "--track", "origin/feat/y")
	runGit(t, repo, "checkout", "-q", "main")
	runGit(t, other, "checkout", "-q", "feat/y")
	writeFile(t, filepath.Join(other, "h.txt"), "h2\n")
	runGit(t, other, "commit", "-q", "-am", "h2")
	runGit(t, other, "push", "-q", "origin", "feat/y")

	if _, err := CheckoutRemoteBranchEnv(repo, "origin/feat/y", nil); err != nil {
		t.Fatalf("CheckoutRemoteBranchEnv: %v", err)
	}
	if cur, _ := CurrentBranch(repo); cur != "feat/y" {
		t.Fatalf("current branch = %q, want feat/y", cur)
	}
	b, err := os.ReadFile(filepath.Join(repo, "h.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(b)) != "h2" {
		t.Fatalf("h.txt = %q, want the remote's newer content (fast-forward)", b)
	}
}

// TestCheckoutRemoteBranch_DivergedRefused: a local branch with its own commit
// must not be silently merged/overwritten; the action fails with a clear error
// and leaves the branch untouched.
func TestCheckoutRemoteBranch_DivergedRefused(t *testing.T) {
	_, repo, other := setupRemotePair(t)
	pushNewBranch(t, other, "feat/z", "i.txt", "remote\n")

	runGit(t, repo, "fetch", "-q", "origin")
	runGit(t, repo, "checkout", "-q", "-b", "feat/z", "--track", "origin/feat/z")
	writeFile(t, filepath.Join(repo, "local.txt"), "local\n")
	runGit(t, repo, "add", "local.txt")
	runGit(t, repo, "commit", "-q", "-m", "local work")
	runGit(t, repo, "checkout", "-q", "main")

	runGit(t, other, "checkout", "-q", "feat/z")
	writeFile(t, filepath.Join(other, "i.txt"), "remote-2\n")
	runGit(t, other, "commit", "-q", "-am", "remote work")
	runGit(t, other, "push", "-q", "origin", "feat/z")

	if _, err := CheckoutRemoteBranchEnv(repo, "origin/feat/z", nil); err == nil {
		t.Fatal("expected a fast-forward refusal for a diverged branch")
	}
	// Local work survives.
	if _, err := os.Stat(filepath.Join(repo, "local.txt")); err != nil {
		t.Fatalf("local commit content lost: %v", err)
	}
}

// TestSplitRemoteBranch covers remote-name disambiguation.
func TestSplitRemoteBranch(t *testing.T) {
	_, repo, _ := setupRemotePair(t)
	if r, b := SplitRemoteBranch(repo, "origin/feat/x"); r != "origin" || b != "feat/x" {
		t.Fatalf("SplitRemoteBranch = (%q,%q), want (origin, feat/x)", r, b)
	}
	if r, b := SplitRemoteBranch(repo, "no-slash"); r != "" || b != "" {
		t.Fatalf("plain name must not parse as remote branch, got (%q,%q)", r, b)
	}
}
