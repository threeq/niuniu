package service

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// TestResolveBaseBranch covers the fallback chain that picks the branch a new
// worktree forks from. Regression: when request was empty and repo had no
// default_branch, the old code fell back to a hardcoded "main", which silently
// failed in repos that use master/develop and never logged the error.
func TestResolveBaseBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}

	root := t.TempDir()
	repoOnFeature := filepath.Join(root, "feature-repo")
	initRepoOnBranch(t, repoOnFeature, "feature/x")

	repoDetached := filepath.Join(root, "detached")
	initRepoOnBranch(t, repoDetached, "main")
	mustGit(t, repoDetached, "checkout", "--detach")

	tests := []struct {
		name      string
		repo      store.Repository
		requested string
		want      string
		wantErr   bool
	}{
		{
			name:      "requested wins",
			repo:      store.Repository{Path: repoOnFeature, DefaultBranch: sql.NullString{String: "main", Valid: true}},
			requested: "develop",
			want:      "develop",
		},
		{
			name:      "empty falls back to default_branch",
			repo:      store.Repository{Path: repoOnFeature, DefaultBranch: sql.NullString{String: "main", Valid: true}},
			requested: "",
			want:      "main",
		},
		{
			name:      "empty default_branch falls back to repo HEAD",
			repo:      store.Repository{Path: repoOnFeature, DefaultBranch: sql.NullString{String: "", Valid: false}},
			requested: "",
			want:      "feature/x",
		},
		{
			name:      "detached HEAD with no defaults yields error",
			repo:      store.Repository{Path: repoDetached, DefaultBranch: sql.NullString{String: "", Valid: false}},
			requested: "",
			wantErr:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveBaseBranch(tc.repo, tc.requested)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got branch=%q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("branch = %q, want %q", got, tc.want)
			}
		})
	}
}

// initRepoOnBranch creates a fresh git repo with one commit and the working
// branch set to startBranch.
func initRepoOnBranch(t *testing.T, dir, startBranch string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mustGit(t, "", "init", "-q", "-b", startBranch, dir)
	mustGit(t, dir, "config", "user.email", "test@example.com")
	mustGit(t, dir, "config", "user.name", "test")
	mustGit(t, dir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	mustGit(t, dir, "add", "f")
	mustGit(t, dir, "commit", "-q", "-m", "init")
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	var cmd *exec.Cmd
	if dir == "" {
		cmd = exec.Command("git", args...)
	} else {
		cmd = exec.Command("git", append([]string{"-C", dir}, args...)...)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}
