package git

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// setupRemotePair builds a bare remote plus two clones so branch push/pull can
// be exercised end-to-end. Returns the bare repo, the primary clone and the
// "other user" clone.
func setupRemotePair(t *testing.T) (bare, repo, other string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	root := t.TempDir()
	bare = filepath.Join(root, "remote.git")
	runGit(t, "", "init", "--bare", "-q", bare)

	repo = filepath.Join(root, "repo")
	runGit(t, "", "init", "-q", "-b", "main", repo)
	for _, c := range [][2]string{{"user.email", "a@example.com"}, {"user.name", "a"}, {"commit.gpgsign", "false"}} {
		runGit(t, repo, "config", c[0], c[1])
	}
	writeFile(t, filepath.Join(repo, "f.txt"), "one\n")
	runGit(t, repo, "add", "f.txt")
	runGit(t, repo, "commit", "-q", "-m", "one")
	runGit(t, repo, "remote", "add", "origin", bare)
	runGit(t, repo, "push", "-q", "-u", "origin", "main")

	other = filepath.Join(root, "other")
	runGit(t, "", "clone", "-q", bare, other)
	for _, c := range [][2]string{{"user.email", "b@example.com"}, {"user.name", "b"}, {"commit.gpgsign", "false"}} {
		runGit(t, other, "config", c[0], c[1])
	}
	return bare, repo, other
}

func TestPushBranch_NonCurrentBranch(t *testing.T) {
	bare, repo, _ := setupRemotePair(t)

	runGit(t, repo, "checkout", "-q", "-b", "feature")
	writeFile(t, filepath.Join(repo, "f.txt"), "feature\n")
	runGit(t, repo, "commit", "-q", "-am", "feature")
	runGit(t, repo, "checkout", "-q", "main")

	if err := PushBranch(repo, "feature"); err != nil {
		t.Fatalf("PushBranch(feature): %v", err)
	}
	if got, want := revParse(t, bare, "feature"), revParse(t, repo, "feature"); got != want {
		t.Fatalf("remote feature = %s, want %s", got, want)
	}
}

func TestPushBranch_CurrentBranch(t *testing.T) {
	bare, repo, _ := setupRemotePair(t)

	writeFile(t, filepath.Join(repo, "f.txt"), "two\n")
	runGit(t, repo, "commit", "-q", "-am", "two")

	if err := PushBranch(repo, "main"); err != nil {
		t.Fatalf("PushBranch(main): %v", err)
	}
	if got, want := revParse(t, bare, "main"), revParse(t, repo, "main"); got != want {
		t.Fatalf("remote main = %s, want %s", got, want)
	}
}

func TestPullBranch_NonCurrentBranchFastForwards(t *testing.T) {
	_, repo, other := setupRemotePair(t)

	runGit(t, other, "checkout", "-q", "-b", "feature")
	writeFile(t, filepath.Join(other, "g.txt"), "shared\n")
	runGit(t, other, "add", "g.txt")
	runGit(t, other, "commit", "-q", "-m", "shared")
	runGit(t, other, "push", "-q", "origin", "feature")

	runGit(t, repo, "fetch", "-q", "origin")
	runGit(t, repo, "branch", "-q", "feature", "origin/feature")
	before := revParse(t, repo, "feature")

	// A newer commit lands on the remote; pulling while on main must move it.
	writeFile(t, filepath.Join(other, "g.txt"), "shared v2\n")
	runGit(t, other, "commit", "-q", "-am", "v2")
	runGit(t, other, "push", "-q", "origin", "feature")

	if err := PullBranch(repo, "feature"); err != nil {
		t.Fatalf("PullBranch(feature): %v", err)
	}
	after := revParse(t, repo, "feature")
	if after == before {
		t.Fatalf("feature did not move: %s", after)
	}
	if got := revParse(t, other, "feature"); got != after {
		t.Fatalf("local feature = %s, want %s", after, got)
	}
}

func TestPullBranch_CurrentBranch(t *testing.T) {
	_, repo, other := setupRemotePair(t)

	writeFile(t, filepath.Join(other, "f.txt"), "remote change\n")
	runGit(t, other, "commit", "-q", "-am", "remote change")
	runGit(t, other, "push", "-q", "origin", "main")

	if err := PullBranch(repo, "main"); err != nil {
		t.Fatalf("PullBranch(main): %v", err)
	}
	if got, want := revParse(t, repo, "main"), revParse(t, other, "main"); got != want {
		t.Fatalf("local main = %s, want %s", got, want)
	}
}
