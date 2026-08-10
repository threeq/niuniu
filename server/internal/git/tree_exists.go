package git

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// tree_exists.go provides read-only existence checks against a git tree, used by
// memory staleness detection to compare a memory's source_path against the
// latest committed/fetched code WITHOUT touching the working tree.

// RevisionResolves reports whether treeish resolves to a commit in the repo at
// repoPath (false for a non-repo, an empty repo, or an unknown/no-upstream ref).
func RevisionResolves(repoPath, treeish string) bool {
	if treeish == "" {
		treeish = "HEAD"
	}
	// --verify --quiet returns non-zero (silently) when the revision is invalid.
	return exec.Command("git", "-C", repoPath, "rev-parse", "--verify", "--quiet", treeish+"^{commit}").Run() == nil
}

// FileExistsInTree reports whether filePath exists in treeish of the repo at
// repoPath. ok=false means it could not be determined (not a git repo, or the
// treeish does not resolve); callers MUST treat ok=false as "unknown" and never
// as "deleted", so transient/ambiguous states never trigger a removal.
func FileExistsInTree(repoPath, treeish, filePath string) (exists bool, ok bool) {
	if treeish == "" {
		treeish = "HEAD"
	}
	if !RevisionResolves(repoPath, treeish) {
		return false, false
	}
	// cat-file -e exits 0 if the object exists, non-zero (an *exec.ExitError) when
	// the path is absent from the tree. A non-exit error means git itself failed.
	err := exec.Command("git", "-C", repoPath, "cat-file", "-e", treeish+":"+filePath).Run()
	if err == nil {
		return true, true
	}
	if _, isExit := err.(*exec.ExitError); isExit {
		return false, true
	}
	return false, false
}

// CloneDepth1 makes a shallow (depth-1, no-tags) clone of srcRepo into destDir.
// For a local source on the same filesystem git uses a cheap local clone, and it
// copies committed state only (never the source's uncommitted working-tree
// changes), which is exactly what a read-only "latest code" snapshot needs.
func CloneDepth1(srcRepo, destDir string) error {
	// Guard against argv flag smuggling: reject operands that look like flags,
	// normalize the source to an absolute path, and pass `--` so git always treats
	// the trailing operands as paths, not options.
	if strings.HasPrefix(srcRepo, "-") {
		return fmt.Errorf("refusing repo path starting with '-': %q", srcRepo)
	}
	if strings.HasPrefix(destDir, "-") {
		return fmt.Errorf("refusing dest path starting with '-': %q", destDir)
	}
	src, err := filepath.Abs(srcRepo)
	if err != nil {
		return fmt.Errorf("resolve repo path: %w", err)
	}
	cmd := exec.Command("git", "clone", "--depth", "1", "--no-tags", "--quiet", "--", src, destDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone --depth 1: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}
