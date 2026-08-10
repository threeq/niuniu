package localrunner

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// sync.go implements code sync (#472): before an exec, the bound directory is
// brought to mirror the remote worktree — check out the remote branch and apply
// the remote's uncommitted diff — while PRESERVING untracked / ignored build
// products (node_modules, target, …). git apply only touches files named in the
// patch and `git checkout` never removes untracked files, so incremental builds
// survive.
//
// The reverse-channel sync request carries no payload, so the runner pulls the
// git state itself from the server over REST via a RemoteStateProvider.

// RepoState is the remote git state for one repository the runner should mirror.
type RepoState struct {
	Name          string // repository name (for logging / multi-repo dirs)
	CurrentBranch string // the worktree's checked-out branch (HEAD)
	BaseBranch    string // the branch the diff is relative to
	Patch         string // concatenated unified diff (base..HEAD + uncommitted)
	CloneURL      string // the registered repo's git remote, "" when none (#478 seed)
}

// RemoteStateProvider fetches the remote git state to mirror. The HTTP
// implementation calls GET /api/workspaces/:id/diff; tests supply a fake.
type RemoteStateProvider interface {
	Fetch(ctx context.Context) ([]RepoState, error)
}

// Syncer brings the bound directory in line with the remote worktree.
type Syncer interface {
	Sync(ctx context.Context) (summary string, err error)
}

// gitRunner runs a git subcommand with optional stdin; overridable in tests so
// the sync logic is exercised without a real git binary.
type gitRunner func(ctx context.Context, dir, stdin string, args ...string) (string, error)

func realGit(ctx context.Context, dir, stdin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// GitSyncer mirrors the remote state into a local git working tree.
type GitSyncer struct {
	dir      string
	provider RemoteStateProvider
	git      gitRunner
}

// NewGitSyncer builds a syncer for the bound directory dir.
func NewGitSyncer(dir string, provider RemoteStateProvider) *GitSyncer {
	return &GitSyncer{dir: dir, provider: provider, git: realGit}
}

// Sync checks out the remote branch and applies the remote uncommitted diff. It
// is best-effort by design: a directory that isn't a git repo, or a patch that
// doesn't apply cleanly, yields a descriptive summary and a non-nil error the
// caller logs — it must NOT hard-block execution, since a stale mirror is still
// runnable and the safety boundary is the gateway, not the sync.
func (s *GitSyncer) Sync(ctx context.Context) (string, error) {
	if s.provider == nil {
		return "sync skipped: no remote state provider", nil
	}
	if _, err := s.git(ctx, s.dir, "", "rev-parse", "--is-inside-work-tree"); err != nil {
		return "sync skipped: bound directory is not a git repository", err
	}
	states, err := s.provider.Fetch(ctx)
	if err != nil {
		return "sync failed: could not fetch remote git state", err
	}
	if len(states) == 0 {
		return "nothing to sync (no remote changes)", nil
	}

	// A single bound directory mirrors one worktree; use the first repo's state.
	// (Multi-repo workspaces bind one directory per repo, one runner each.)
	st := states[0]
	var summary []string

	if st.CurrentBranch != "" {
		if _, err := s.git(ctx, s.dir, "", "checkout", st.CurrentBranch); err != nil {
			return "sync failed: could not checkout " + st.CurrentBranch, err
		}
		summary = append(summary, "checked out "+st.CurrentBranch)
	}

	if strings.TrimSpace(st.Patch) != "" {
		// --3way lets the apply fall back to a merge when context drifted;
		// git apply never deletes untracked files, so build products survive.
		if _, err := s.git(ctx, s.dir, st.Patch, "apply", "--3way", "--whitespace=nowarn"); err != nil {
			return "sync failed: remote diff did not apply cleanly", err
		}
		summary = append(summary, "applied remote uncommitted diff")
	} else {
		summary = append(summary, "no uncommitted remote diff")
	}

	return strings.Join(summary, "; "), nil
}
