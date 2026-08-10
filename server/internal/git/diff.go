package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// FileDiff represents the diff for a single file.
type FileDiff struct {
	Path      string     `json:"path"`
	Status    string     `json:"status"` // added|modified|deleted|renamed
	Additions int        `json:"additions"`
	Deletions int        `json:"deletions"`
	Hunks     []DiffHunk `json:"hunks"`
	RawPatch  string     `json:"raw_patch"`
}

// DiffHunk represents a single hunk in a diff.
type DiffHunk struct {
	OldStart int    `json:"old_start"`
	OldCount int    `json:"old_count"`
	NewStart int    `json:"new_start"`
	NewCount int    `json:"new_count"`
	Content  string `json:"content"`
}

// Diff returns the tracked changes for a worktree relative to a baseline and
// parses them into structured data.
//
// When base is non-empty it diffs the merge-base of base and HEAD against the
// current working tree. That yields the three-dot "base...HEAD" view (changes
// made on this branch since it diverged from base), extended to also include
// uncommitted work — i.e. all committed + uncommitted tracked changes vs the
// baseline. When base is empty (or it cannot be resolved) it falls back to
// "git diff HEAD" (uncommitted tracked changes only), preserving the original
// behavior for callers that only care about pre-commit output.
//
// Neither form lists untracked files; callers building a full "vs baseline"
// file list merge those in via UntrackedDiffs.
func Diff(worktreePath, base string) ([]FileDiff, error) {
	rev := mergeBaseDiffRev(worktreePath, base)
	if rev == "" {
		rev = "HEAD"
	}

	cmd := exec.Command("git", "-C", worktreePath, "diff", rev)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}

	return parseDiff(string(out))
}

// ResolveDiffBase picks a base revision to diff a worktree against, trying the
// preferred base first and then common mainline names (main/master and their
// origin/* forms). It returns the first candidate that has a merge-base with
// HEAD — i.e. a real, related ref — or "" if none do.
//
// This is the fallback for worktrees whose recorded base branch is unreliable
// (e.g. a stale association): without it git.Diff would silently collapse to
// "git diff HEAD" (uncommitted changes only), under-reporting the true change
// magnitude. Diffing against main instead surfaces everything since the fork.
func ResolveDiffBase(worktreePath, preferred string) string {
	seen := make(map[string]bool, 6)
	for _, c := range []string{preferred, "main", "master", "origin/HEAD", "origin/main", "origin/master"} {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		if _, err := exec.Command("git", "-C", worktreePath, "merge-base", c, "HEAD").Output(); err == nil {
			return c
		}
	}
	return ""
}

// mergeBaseDiffRev resolves the revision to diff the working tree against so the
// result equals "base...HEAD" plus uncommitted changes: the merge-base of base
// and HEAD. Returns "" when base is empty or cannot be resolved (e.g. the base
// branch no longer exists), signalling the caller to fall back to "HEAD".
func mergeBaseDiffRev(worktreePath, base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", worktreePath, "merge-base", base, "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// UntrackedDiffs returns synthetic added-file diffs for every untracked file in
// the worktree. The committed/uncommitted diff produced by Diff never lists
// untracked files, so callers assembling a full "vs baseline" view merge these
// in. Ignored files are excluded; binary files are reported correctly by the
// underlying "git diff --no-index".
func UntrackedDiffs(worktreePath string) ([]FileDiff, error) {
	cmd := exec.Command("git", "-C", worktreePath, "-c", "core.quotepath=false",
		"status", "--porcelain", "--untracked-files=all")
	cmd.Env = append(os.Environ(), "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git status: %w", err)
	}

	var diffs []FileDiff
	for _, line := range strings.Split(strings.TrimRight(string(out), " \r\n"), "\n") {
		// Porcelain v1 marks untracked entries with the "?? " prefix.
		if !strings.HasPrefix(line, "?? ") {
			continue
		}
		path := decodeQuotedPath(strings.TrimSpace(line[3:]))
		if path == "" {
			continue
		}
		fd, err := untrackedFileDiff(worktreePath, path)
		if err != nil {
			return nil, err
		}
		if fd != nil {
			diffs = append(diffs, *fd)
		}
	}
	return diffs, nil
}

// untrackedFileDiff builds an added-file FileDiff for a single untracked path
// using "git diff --no-index" against /dev/null, which produces a normal
// unified diff (and reports binary files as "Binary files ... differ").
func untrackedFileDiff(worktreePath, filePath string) (*FileDiff, error) {
	cmd := exec.Command("git", "-C", worktreePath, "-c", "core.quotepath=false",
		"diff", "--no-index", "--", "/dev/null", filePath)
	out, err := cmd.Output()
	// --no-index uses diff(1) exit semantics: 1 means "files differ" (always the
	// case here, since the source is empty). Only a different code is a real error.
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
			return nil, fmt.Errorf("git diff --no-index %s: %w", filePath, err)
		}
	}

	diffs, err := parseDiff(string(out))
	if err != nil {
		return nil, err
	}
	if len(diffs) == 0 {
		return nil, nil
	}
	fd := diffs[0]
	fd.Status = "added"
	fd.Path = filePath
	return &fd, nil
}

// parseDiff parses the unified diff output into structured FileDiff objects.
func parseDiff(diffOutput string) ([]FileDiff, error) {
	if strings.TrimSpace(diffOutput) == "" {
		return []FileDiff{}, nil
	}

	// Split by file boundaries
	filePattern := regexp.MustCompile(`(?m)^diff --git a/(.+?) b/(.+?)$`)
	matches := filePattern.FindAllStringSubmatchIndex(diffOutput, -1)

	if len(matches) == 0 {
		return []FileDiff{}, nil
	}

	var diffs []FileDiff

	for i, match := range matches {
		// Extract file path from the match
		pathStart := match[4]
		pathEnd := match[5]
		filePath := diffOutput[pathStart:pathEnd]

		// Determine the content for this file
		var content string
		if i < len(matches)-1 {
			content = diffOutput[match[0]:matches[i+1][0]]
		} else {
			content = diffOutput[match[0]:]
		}

		// Determine status
		status := "modified"
		if strings.Contains(content, "new file mode") {
			status = "added"
		} else if strings.Contains(content, "deleted file mode") {
			status = "deleted"
		} else if strings.Contains(content, "rename from") {
			status = "renamed"
		}

		// Count additions and deletions
		additions, deletions := countAdditionsDeletions(content)

		// Parse hunks
		hunks := parseHunks(content)

		diffs = append(diffs, FileDiff{
			Path:      filePath,
			Status:    status,
			Additions: additions,
			Deletions: deletions,
			Hunks:     hunks,
			RawPatch:  content,
		})
	}

	return diffs, nil
}

// countAdditionsDeletions counts the number of addition and deletion lines in a diff.
func countAdditionsDeletions(content string) (additions, deletions int) {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		// Additions: lines starting with + but not +++ (binary file indicator)
		if line[0] == '+' && !strings.HasPrefix(line, "+++") {
			additions++
		}
		// Deletions: lines starting with - but not --- (binary file indicator)
		if line[0] == '-' && !strings.HasPrefix(line, "---") {
			deletions++
		}
	}
	return
}

// parseHunks extracts hunk information from a file diff.
func parseHunks(content string) []DiffHunk {
	hunkPattern := regexp.MustCompile(`@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)
	matches := hunkPattern.FindAllStringSubmatchIndex(content, -1)

	var hunks []DiffHunk

	for i, match := range matches {
		// Extract hunk header values
		oldStart := parseInt(content[match[2]:match[3]])
		oldCount := 1
		if match[4] != -1 && match[5] != -1 {
			oldCount = parseInt(content[match[4]:match[5]])
		}

		newStart := parseInt(content[match[6]:match[7]])
		newCount := 1
		if match[8] != -1 && match[9] != -1 {
			newCount = parseInt(content[match[8]:match[9]])
		}

		// Extract hunk content
		var hunkContent string
		if i < len(matches)-1 {
			hunkContent = content[match[0]:matches[i+1][0]]
		} else {
			hunkContent = content[match[0]:]
		}

		hunks = append(hunks, DiffHunk{
			OldStart: oldStart,
			OldCount: oldCount,
			NewStart: newStart,
			NewCount: newCount,
			Content:  hunkContent,
		})
	}

	return hunks
}

// parseInt safely parses a string to int, returning 0 on error.
func parseInt(s string) int {
	var result int
	fmt.Sscanf(s, "%d", &result)
	return result
}

// FileContent returns the content of a file at a given path.
// The path must be within the worktree (no path traversal).
func FileContent(worktreePath, filePath string) (string, error) {
	// Resolve the absolute path and verify it's within worktree
	absPath := filepath.Join(worktreePath, filePath)
	absWorktree, err := filepath.Abs(worktreePath)
	if err != nil {
		return "", fmt.Errorf("resolve worktree path: %w", err)
	}
	// Clean the path and verify it starts with worktree prefix
	cleanPath := filepath.Clean(absPath)
	if !strings.HasPrefix(cleanPath, absWorktree) {
		return "", fmt.Errorf("path escape attempt detected")
	}

	cmd := exec.Command("git", "-C", worktreePath, "show", "HEAD:"+filePath)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git show: %w", err)
	}
	return string(out), nil
}

// DiffFile returns the diff for a single file relative to a baseline, using the
// same merge-base semantics as Diff (see Diff for how base is interpreted).
// This is more efficient than getting the full diff and filtering. Untracked
// files have no entry in the tracked diff, so they fall back to a synthetic
// added-file diff. The path must be within the worktree (no path traversal).
func DiffFile(worktreePath, base, filePath string) (*FileDiff, error) {
	// Resolve the absolute path and verify it's within worktree
	absPath := filepath.Join(worktreePath, filePath)
	absWorktree, err := filepath.Abs(worktreePath)
	if err != nil {
		return nil, fmt.Errorf("resolve worktree path: %w", err)
	}
	// Clean the path and verify it starts with worktree prefix
	cleanPath := filepath.Clean(absPath)
	if !strings.HasPrefix(cleanPath, absWorktree) {
		return nil, fmt.Errorf("path escape attempt detected")
	}

	rev := mergeBaseDiffRev(worktreePath, base)
	if rev == "" {
		rev = "HEAD"
	}

	cmd := exec.Command("git", "-C", worktreePath, "diff", rev, "--", filePath)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}

	diffs, err := parseDiff(string(out))
	if err != nil {
		return nil, err
	}

	if len(diffs) == 0 {
		// The file has no tracked diff vs base. If it is untracked, surface it
		// as an added file so the single-file viewer matches the file list.
		if isUntracked(worktreePath, filePath) {
			fd, uerr := untrackedFileDiff(worktreePath, filePath)
			if uerr == nil && fd != nil {
				return fd, nil
			}
		}
		return nil, fmt.Errorf("file not found in diff: %s", filePath)
	}
	return &diffs[0], nil
}

// isUntracked reports whether filePath is an untracked (and not ignored) file in
// the worktree.
func isUntracked(worktreePath, filePath string) bool {
	out, err := exec.Command("git", "-C", worktreePath, "-c", "core.quotepath=false",
		"ls-files", "--others", "--exclude-standard", "--", filePath).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}
