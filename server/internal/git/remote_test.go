package git

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSamePath(t *testing.T) {
	caseInsensitive := runtime.GOOS == "windows" || runtime.GOOS == "darwin"
	cases := []struct {
		a, b string
		want bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b/", "/a/b", true},           // cleaned
		{"/a/./b", "/a/b", true},          // cleaned
		{"", "/a", false},                 // empty never matches
		{"/a", "", false},                 // empty never matches
		{"/a/b", "/a/c", false},           // genuinely different
		{"/a/B", "/a/b", caseInsensitive}, // case folds only on win/darwin
	}
	for _, c := range cases {
		if got := SamePath(c.a, c.b); got != c.want {
			t.Errorf("SamePath(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestResolveDiffBase(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}

	t.Run("prefers a resolvable preferred base, else falls back to main", func(t *testing.T) {
		dir := t.TempDir()
		runGit(t, "", "init", "-q", "-b", "main", dir)
		runGit(t, dir, "config", "user.email", "test@example.com")
		runGit(t, dir, "config", "user.name", "test")
		runGit(t, dir, "config", "commit.gpgsign", "false")
		writeFile(t, filepath.Join(dir, "f.txt"), "a\n")
		runGit(t, dir, "add", "-A")
		runGit(t, dir, "commit", "-q", "-m", "init")
		runGit(t, dir, "checkout", "-q", "-b", "feat")

		// A real preferred base wins.
		if got := ResolveDiffBase(dir, "main"); got != "main" {
			t.Errorf("ResolveDiffBase(preferred=main) = %q, want main", got)
		}
		// An unresolvable preferred base falls through to main.
		if got := ResolveDiffBase(dir, "does-not-exist"); got != "main" {
			t.Errorf("ResolveDiffBase(preferred=missing) = %q, want main (fallback)", got)
		}
		// Empty preferred falls through to main.
		if got := ResolveDiffBase(dir, ""); got != "main" {
			t.Errorf("ResolveDiffBase(preferred=\"\") = %q, want main", got)
		}
	})

	t.Run("returns empty when no candidate resolves", func(t *testing.T) {
		dir := t.TempDir()
		// Default branch is "work" — no main/master/origin exists.
		runGit(t, "", "init", "-q", "-b", "work", dir)
		runGit(t, dir, "config", "user.email", "test@example.com")
		runGit(t, dir, "config", "user.name", "test")
		runGit(t, dir, "config", "commit.gpgsign", "false")
		writeFile(t, filepath.Join(dir, "f.txt"), "a\n")
		runGit(t, dir, "add", "-A")
		runGit(t, dir, "commit", "-q", "-m", "init")

		if got := ResolveDiffBase(dir, ""); got != "" {
			t.Errorf("ResolveDiffBase with no mainline ref = %q, want \"\"", got)
		}
	})
}

func TestResolveWorktreeSource(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}

	src := t.TempDir()
	runGit(t, "", "init", "-q", "-b", "main", src)
	runGit(t, src, "config", "user.email", "test@example.com")
	runGit(t, src, "config", "user.name", "test")
	runGit(t, src, "config", "commit.gpgsign", "false")
	runGit(t, src, "remote", "add", "origin", "https://example.com/acme/widget.git")
	writeFile(t, filepath.Join(src, "f.txt"), "a\n")
	runGit(t, src, "add", "-A")
	runGit(t, src, "commit", "-q", "-m", "init")

	t.Run("plain clone reports remote + its own path as source", func(t *testing.T) {
		got := ResolveWorktreeSource(src)
		if got.RemoteURL != "https://example.com/acme/widget.git" {
			t.Errorf("RemoteURL = %q", got.RemoteURL)
		}
		if !SamePath(got.SourcePath, src) {
			t.Errorf("SourcePath = %q, want %q", got.SourcePath, src)
		}
	})

	t.Run("linked worktree resolves back to the source repo", func(t *testing.T) {
		linked := filepath.Join(t.TempDir(), "wt")
		// New branch (main is already checked out in the source working tree).
		runGit(t, src, "worktree", "add", "-q", "-b", "wt", linked)
		got := ResolveWorktreeSource(linked)
		if got.RemoteURL != "https://example.com/acme/widget.git" {
			t.Errorf("linked RemoteURL = %q", got.RemoteURL)
		}
		// --git-common-dir from the linked worktree points back at the source repo.
		if !SamePath(got.SourcePath, src) {
			t.Errorf("linked SourcePath = %q, want source %q", got.SourcePath, src)
		}
	})

	t.Run("non-git directory yields empty best-effort result", func(t *testing.T) {
		got := ResolveWorktreeSource(t.TempDir())
		if got.RemoteURL != "" || got.SourcePath != "" {
			t.Errorf("non-git dir = %+v, want zero value", got)
		}
	})
}
