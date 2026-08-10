package git

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// WorktreeSource describes where a worktree's history actually lives. It is used
// to reverse-resolve which registered repository a worktree belongs to when its
// stored association is stale (e.g. the source repo was deleted and re-added
// under a new id, leaving the worktree row pointing at a vanished repository).
type WorktreeSource struct {
	// RemoteURL is the `origin` remote URL of the worktree's repository, if any.
	RemoteURL string
	// SourcePath is the absolute path of the source working tree (the main clone)
	// that owns this worktree's shared .git store. For a plain clone this is the
	// clone itself; for a linked `git worktree` it is the parent repository.
	SourcePath string
}

// ResolveWorktreeSource inspects a worktree directory and returns its source
// identity. All git failures are swallowed (the corresponding field is left
// empty) so callers can treat "unknown" uniformly — this is a best-effort
// reverse lookup, never a hard dependency.
func ResolveWorktreeSource(worktreePath string) WorktreeSource {
	var src WorktreeSource

	if out, err := exec.Command("git", "-C", worktreePath, "remote", "get-url", "origin").Output(); err == nil {
		src.RemoteURL = strings.TrimSpace(string(out))
	}

	// --git-common-dir resolves to the SHARED .git directory — the source repo's
	// .git even when called from a linked worktree. Its parent is the source
	// working tree. The path may come back relative to the worktree, so anchor it.
	if out, err := exec.Command("git", "-C", worktreePath, "rev-parse", "--git-common-dir").Output(); err == nil {
		commonDir := strings.TrimSpace(string(out))
		if commonDir != "" {
			if !filepath.IsAbs(commonDir) {
				commonDir = filepath.Join(worktreePath, commonDir)
			}
			src.SourcePath = filepath.Clean(filepath.Dir(commonDir))
		}
	}

	return src
}

// SamePath reports whether two filesystem paths refer to the same location,
// tolerant of separator/cleanup differences. It is a textual comparison — it
// does not stat the paths. Case folding is applied only on case-insensitive
// platforms (Windows/macOS); on Linux "/srv/Repo" and "/srv/repo" are distinct.
func SamePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ca, cb := filepath.Clean(a), filepath.Clean(b)
	if ca == cb {
		return true
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(ca, cb)
	}
	return false
}
