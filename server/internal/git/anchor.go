package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Anchoring plumbing for review comments (#683 wave 1).
//
// A review comment that stores only a line number silently re-points at whatever
// occupies that line after the file changes — the reader sees a confident anchor
// on unrelated code. To avoid that, a comment records the commit it was written
// against plus the blob hash and a snapshot of the surrounding source, and the
// resolver re-derives the anchor on read: relocate when the snapshot is still
// findable, mark outdated when it is not. Never silently drift.

// HeadSHA returns the worktree's current HEAD commit, or "" when HEAD cannot be
// resolved (an empty repo with no commits yet). Callers treat "" as "unknown
// version" rather than an error — an unanchored comment is still worth storing.
func HeadSHA(worktreePath string) string {
	out, err := exec.Command("git", "-C", worktreePath, "rev-parse", "--verify", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// WorkingBlobSHA returns the git blob hash of a file's CURRENT working-tree
// content — the bytes the reviewer actually saw, which for an uncommitted edit
// differ from any committed blob. Returns "" when the file cannot be read or
// hashed (missing, unreadable, or outside the worktree).
//
// hash-object is used rather than `git rev-parse HEAD:path` deliberately: the
// review UI shows the working tree, so the anchor must pin the working tree.
func WorkingBlobSHA(worktreePath, filePath string) string {
	abs, ok := resolveInWorktree(worktreePath, filePath)
	if !ok {
		return ""
	}
	if _, err := os.Stat(abs); err != nil {
		return ""
	}
	out, err := exec.Command("git", "-C", worktreePath, "hash-object", "--", abs).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// WorkingFileLines returns a file's current working-tree content split into
// lines (no trailing-newline artifact), for snapshotting context and for
// relocating an existing snapshot. ok is false when the file is absent or
// unreadable — a deleted file cannot host a relocated anchor.
func WorkingFileLines(worktreePath, filePath string) (lines []string, ok bool) {
	abs, valid := resolveInWorktree(worktreePath, filePath)
	if !valid {
		return nil, false
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, false
	}
	return SplitLines(string(data)), true
}

// SplitLines splits file content into lines, normalising CRLF and dropping the
// artifact empty element a trailing newline produces. Exported so the anchor
// resolver splits stored snapshots exactly the way it split the file they came
// from — a mismatch there would make every relocation fail.
func SplitLines(content string) []string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.TrimSuffix(content, "\n")
	if content == "" {
		return nil
	}
	return strings.Split(content, "\n")
}

// resolveInWorktree joins a repo-relative path onto the worktree and rejects
// escapes (the same containment check the diff helpers apply). It compares
// cleaned absolute paths with a separator boundary, so a sibling directory
// sharing a name prefix ("/repo-evil" vs "/repo") is not accepted as inside.
func resolveInWorktree(worktreePath, filePath string) (string, bool) {
	absRoot, err := filepath.Abs(worktreePath)
	if err != nil {
		return "", false
	}
	absPath := filepath.Clean(filepath.Join(absRoot, filePath))
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return absPath, true
}
