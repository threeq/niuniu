package localrunner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// seeder.go implements seed-clone (#478): when a freshly-bound directory is
// EMPTY, the runner populates it from the workspace's registered repositories so
// the AI has something to operate on. The clone URL is the repo's own git remote
// (carried on the workspace diff payload as clone_url); the clone runs through
// the host's plain `git`, so authentication is whatever the user's machine
// already has (credential helper / SSH keys) — no niuniu token is spent on the
// clone itself.
//
// Boundaries, by design:
//   - Seeding NEVER touches a non-empty directory (no clobber of an existing
//     checkout). "绑定目录为空时" is a hard precondition.
//   - It is best-effort: any failure yields a descriptive summary + error the
//     caller logs, and must not block bringing the runner online (an unseeded
//     dir is still an operable directory the AI can populate via local_exec).
//   - A single seedable repo clones into the bound dir itself, preserving the
//     子D single-repo sync model (checkout + apply diff mirrors it afterwards).
//     Multiple repos each clone into a per-name subdirectory — the "AI 自建/
//     管理多个 repo" layout, where the bound dir is a parent, not a repo.
type Seeder struct {
	dir      string
	provider RemoteStateProvider
	git      gitRunner
}

// NewSeeder builds a seeder for the bound directory dir, reusing the same remote
// state provider the syncer uses (GET /api/workspaces/:id/diff carries clone_url).
func NewSeeder(dir string, provider RemoteStateProvider) *Seeder {
	return &Seeder{dir: dir, provider: provider, git: realGit}
}

// Seed clones the workspace's repositories into an empty bound directory. It is
// a no-op (nil error) when the directory is not empty or no clone URL is known.
func (s *Seeder) Seed(ctx context.Context) (string, error) {
	if s.provider == nil {
		return "seed skipped: no remote state provider", nil
	}
	empty, err := dirIsEmpty(s.dir)
	if err != nil {
		return "seed skipped: could not inspect bound directory", err
	}
	if !empty {
		return "seed skipped: bound directory is not empty", nil
	}

	states, err := s.provider.Fetch(ctx)
	if err != nil {
		return "seed failed: could not fetch repository seed state", err
	}
	seedable := make([]RepoState, 0, len(states))
	for _, st := range states {
		if strings.TrimSpace(st.CloneURL) != "" {
			seedable = append(seedable, st)
		}
	}
	if len(seedable) == 0 {
		return "seed skipped: no clone URL configured (empty operable directory)", nil
	}

	// Single repo → clone into the bound dir itself so the syncer's single-repo
	// model keeps working. Multiple repos → one subdir each (AI-managed layout).
	single := len(seedable) == 1
	var done []string
	for _, st := range seedable {
		dest := s.dir
		if !single {
			// The subdir name is server-supplied; require a single safe path
			// segment so a crafted repo name can never escape the bound dir
			// (mirrors the gateway's no-`..` boundary).
			if !safeSegment(st.Name) {
				return "seed failed: unsafe repository name " + st.Name, fmt.Errorf("unsafe repo name %q", st.Name)
			}
			dest = filepath.Join(s.dir, st.Name)
		}
		if _, err := s.git(ctx, "", "", "clone", st.CloneURL, dest); err != nil {
			return "seed failed: could not clone " + st.Name, err
		}
		// Materialize the workspace branch locally so a subsequent sync's
		// `checkout <branch>` + apply-diff lands on the right ref. -B creates or
		// resets it from the freshly-cloned HEAD; harmless when it already exists.
		if st.CurrentBranch != "" {
			if _, err := s.git(ctx, dest, "", "checkout", "-B", st.CurrentBranch); err != nil {
				return "seed failed: cloned " + st.Name + " but could not create branch " + st.CurrentBranch, err
			}
		}
		done = append(done, st.Name)
	}
	return fmt.Sprintf("seeded %d repo(s): %s", len(done), strings.Join(done, ", ")), nil
}

// safeSegment reports whether name is a single, non-escaping path component
// usable as a subdirectory (no separators, not "." / ".." / empty).
func safeSegment(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return !strings.ContainsAny(name, `/\`)
}

// dirIsEmpty reports whether dir has no entries. A missing directory counts as
// empty (git clone creates it); any read error other than not-exist surfaces.
func dirIsEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	}
	return len(entries) == 0, nil
}
