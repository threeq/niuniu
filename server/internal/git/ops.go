package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// FileStatus represents the status of a file in the working tree.
type FileStatus struct {
	Path   string `json:"path"`
	Status string `json:"status"` // M, A, D, ??, etc.
}

// LogEntry represents a single commit log entry.
type LogEntry struct {
	Hash    string `json:"hash"`
	Author  string `json:"author"`
	Date    string `json:"date"`
	Message string `json:"message"`
}

// WorktreeChangeStatus represents change details for a worktree.
type WorktreeChangeStatus struct {
	WorktreePath       string       `json:"worktree_path"`
	RepoName           string       `json:"repo_name"`
	Branch             string       `json:"branch"`
	BaseBranch         string       `json:"base_branch"`
	UnstagedFiles      []FileStatus `json:"unstaged_files"`
	StagedFiles        []FileStatus `json:"staged_files"`
	AheadCount         int          `json:"ahead_count"`
	AheadCommits       []LogEntry   `json:"ahead_commits"`
	AheadOfBaseCount   int          `json:"ahead_of_base_count"`
	AheadOfBaseCommits []LogEntry   `json:"ahead_of_base_commits"`
	HasMergeConflict   bool         `json:"has_merge_conflict"`
	UnmergedFiles      []FileStatus `json:"unmerged_files"`
}

// Status returns the status of files in the working tree.
func Status(worktreePath string) ([]FileStatus, error) {
	cmd := exec.Command("git", "-C", worktreePath, "status", "--porcelain")
	// Ensure UTF-8 output on all platforms
	cmd.Env = append(os.Environ(), "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git status: %w", err)
	}

	output := strings.TrimRight(string(out), " \r\n")
	if output == "" {
		return []FileStatus{}, nil
	}

	lines := strings.Split(output, "\n")
	statuses := make([]FileStatus, 0, len(lines))

	for _, line := range lines {
		if len(line) < 3 {
			continue
		}

		// Status format: "XY PATH" where XY is the status code
		status := strings.TrimSpace(line[:2])
		path := strings.TrimSpace(line[3:])
		// Decode quoted paths (git uses C-style quoting for non-ASCII filenames on Windows)
		path = decodeQuotedPath(path)

		statuses = append(statuses, FileStatus{
			Path:   path,
			Status: status,
		})
	}

	return statuses, nil
}

// decodeQuotedPath decodes git's C-style quoted path (used for non-ASCII filenames on Windows)
func decodeQuotedPath(path string) string {
	// Git uses C-style quoting for paths with non-ASCII or special characters:
	// - Wrapped in double quotes
	// - Backslash escapes for special chars: \a, \b, \f, \n, \r, \t, \v, \\
	// - Octal escapes for bytes: \NNN (three digits)
	// - \uNNNN for Unicode code points (in newer git versions)

	if !strings.HasPrefix(path, "\"") || !strings.HasSuffix(path, "\"") {
		return path
	}

	// Strip the outer quotes
	path = path[1 : len(path)-1]
	result := make([]byte, 0, len(path))
	i := 0

	for i < len(path) {
		if path[i] == '\\' && i+1 < len(path) {
			next := path[i+1]
			switch next {
			case 'a':
				result = append(result, '\a')
				i += 2
			case 'b':
				result = append(result, '\b')
				i += 2
			case 'f':
				result = append(result, '\f')
				i += 2
			case 'n':
				result = append(result, '\n')
				i += 2
			case 'r':
				result = append(result, '\r')
				i += 2
			case 't':
				result = append(result, '\t')
				i += 2
			case 'v':
				result = append(result, '\v')
				i += 2
			case '\\':
				result = append(result, '\\')
				i += 2
			case 'u', 'U': // Unicode escapes
				if next == 'u' && i+4 < len(path) {
					// \uNNNN (4 hex digits)
					val := parseHex(path[i+2 : i+6])
					if val > 0 {
						result = appendRune(result, rune(val))
					}
					i += 6
				} else if next == 'U' && i+8 < len(path) {
					// \UNNNNNNNN (8 hex digits)
					val := parseHex(path[i+2 : i+10])
					if val > 0 {
						result = appendRune(result, rune(val))
					}
					i += 10
				} else {
					result = append(result, path[i])
					i++
				}
			default:
				// Octal escape \NNN (1-3 digits)
				if next >= '0' && next <= '7' {
					j := i + 2
					for j < i+4 && j < len(path) && path[j] >= '0' && path[j] <= '7' {
						j++
					}
					val := parseOctal(path[i+1 : j])
					result = append(result, val)
					i = j
				} else {
					result = append(result, path[i])
					i++
				}
			}
		} else {
			result = append(result, path[i])
			i++
		}
	}

	return string(result)
}

func parseOctal(s string) byte {
	var val byte
	for _, c := range s {
		val = val*8 + byte(c-'0')
	}
	return val
}

func parseHex(s string) rune {
	var val rune
	for _, c := range s {
		if c >= '0' && c <= '9' {
			val = val*16 + rune(c-'0')
		} else if c >= 'a' && c <= 'f' {
			val = val*16 + rune(c-'a'+10)
		} else if c >= 'A' && c <= 'F' {
			val = val*16 + rune(c-'A'+10)
		}
	}
	return val
}

func appendRune(b []byte, r rune) []byte {
	// Encode rune as UTF-8
	if r < 0x80 {
		return append(b, byte(r))
	} else if r < 0x800 {
		return append(b, byte(0xC0|r>>6), byte(0x80|(r&0x3F)))
	} else if r < 0x10000 {
		return append(b, byte(0xE0|r>>12), byte(0x80|(r>>6&0x3F)), byte(0x80|(r&0x3F)))
	} else {
		return append(b, byte(0xF0|r>>18), byte(0x80|(r>>12&0x3F)), byte(0x80|(r>>6&0x3F)), byte(0x80|(r&0x3F)))
	}
}

// Log returns commit history for the repository.
func Log(worktreePath string, limit int) ([]LogEntry, error) {
	args := []string{"-C", worktreePath, "log", "--format=%H|%an|%ai|%s"}
	if limit > 0 {
		args = append(args, "-n", fmt.Sprintf("%d", limit))
	}

	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}

	output := strings.TrimSpace(string(out))
	if output == "" {
		return []LogEntry{}, nil
	}

	lines := strings.Split(output, "\n")
	entries := make([]LogEntry, 0, len(lines))

	for _, line := range lines {
		parts := strings.SplitN(line, "|", 4)
		if len(parts) != 4 {
			continue
		}

		entries = append(entries, LogEntry{
			Hash:    parts[0],
			Author:  parts[1],
			Date:    parts[2],
			Message: parts[3],
		})
	}

	return entries, nil
}

// LogRange returns commit history between two refs (e.g., "main..feature").
func LogRange(worktreePath string, rangeExpr string) ([]LogEntry, error) {
	args := []string{"-C", worktreePath, "log", "--format=%H|%an|%ai|%s", rangeExpr}

	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log %s: %w", rangeExpr, err)
	}

	output := strings.TrimSpace(string(out))
	if output == "" {
		return []LogEntry{}, nil
	}

	lines := strings.Split(output, "\n")
	entries := make([]LogEntry, 0, len(lines))

	for _, line := range lines {
		parts := strings.SplitN(line, "|", 4)
		if len(parts) != 4 {
			continue
		}

		entries = append(entries, LogEntry{
			Hash:    parts[0],
			Author:  parts[1],
			Date:    parts[2],
			Message: parts[3],
		})
	}

	return entries, nil
}

// WorktreeChangeCheck collects change details for a worktree.
func WorktreeChangeCheck(worktreePath, repoName, branch, baseBranch string) WorktreeChangeStatus {
	status := WorktreeChangeStatus{
		WorktreePath:       worktreePath,
		RepoName:           repoName,
		Branch:             branch,
		BaseBranch:         baseBranch,
		UnstagedFiles:      []FileStatus{},
		StagedFiles:        []FileStatus{},
		UnmergedFiles:      []FileStatus{},
		AheadCommits:       []LogEntry{},
		AheadOfBaseCommits: []LogEntry{},
	}

	// Get porcelain status — parse raw output directly instead of using Status()
	// because Status() applies TrimSpace which collapses " M" and "M " both to "M",
	// losing the XY column distinction needed to classify staged vs unstaged.
	cmd := exec.Command("git", "-C", worktreePath, "status", "--porcelain")
	cmd.Env = append(os.Environ(), "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8")
	out, _ := cmd.Output()
	lines := strings.Split(strings.TrimRight(string(out), " \r\n"), "\n")
	for _, line := range lines {
		if len(line) < 3 {
			continue
		}
		xy := line[:2]
		x := xy[0] // index (staged)
		y := xy[1] // worktree (unstaged)
		path := strings.TrimSpace(line[3:])
		path = decodeQuotedPath(path)

		fs := FileStatus{Path: path, Status: xy}

		// Check for merge conflicts
		isUnmerged := (x == 'U' || y == 'U') || xy == "AA" || xy == "DD" || xy == "AU" || xy == "UA" || xy == "DU" || xy == "UD"
		if isUnmerged {
			status.HasMergeConflict = true
			status.UnmergedFiles = append(status.UnmergedFiles, fs)
			continue
		}

		// Staged: X is non-space and non-'?'
		if x != ' ' && x != '?' {
			status.StagedFiles = append(status.StagedFiles, fs)
		}

		// Unstaged: Y is non-space and non-'?'
		if y != ' ' && y != '?' {
			status.UnstagedFiles = append(status.UnstagedFiles, fs)
		}

		// Untracked: status is "??"
		if xy == "??" {
			status.UnstagedFiles = append(status.UnstagedFiles, fs)
		}
	}

	// Commits ahead of remote
	aheadRemote, _ := AheadCount(worktreePath, "origin/"+branch, branch)
	status.AheadCount = aheadRemote
	if aheadRemote > 0 {
		commits, _ := LogRange(worktreePath, "origin/"+branch+".."+branch)
		status.AheadCommits = commits
	}

	// Commits ahead of base branch (not merged to base)
	if baseBranch != "" && baseBranch != branch {
		aheadBase, _ := AheadCount(worktreePath, baseBranch, branch)
		status.AheadOfBaseCount = aheadBase
		if aheadBase > 0 {
			commits, _ := LogRange(worktreePath, baseBranch+".."+branch)
			status.AheadOfBaseCommits = commits
		}
	}

	return status
}

// WorktreeHasChanges returns true if the WorktreeChangeStatus has any changes.
func WorktreeHasChanges(s WorktreeChangeStatus) bool {
	return len(s.UnstagedFiles) > 0 ||
		len(s.StagedFiles) > 0 ||
		s.AheadCount > 0 ||
		s.AheadOfBaseCount > 0 ||
		s.HasMergeConflict
}

// DiscardAll discards all staged and unstaged changes, and removes untracked files.
func DiscardAll(worktreePath string) error {
	// Unstage all staged changes first
	resetCmd := exec.Command("git", "-C", worktreePath, "reset", "HEAD")
	resetCmd.CombinedOutput() // ignore error (fails if no commits yet)

	checkoutCmd := exec.Command("git", "-C", worktreePath, "checkout", "--", ".")
	if out, err := checkoutCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout: %s: %w", strings.TrimSpace(string(out)), err)
	}
	cleanCmd := exec.Command("git", "-C", worktreePath, "clean", "-fd")
	if out, err := cleanCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clean: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// DiscardFile discards changes for a single file. For untracked files, deletes them.
func DiscardFile(worktreePath, filePath string) error {
	absWorktree, err := filepath.Abs(worktreePath)
	if err != nil {
		return fmt.Errorf("resolve worktree path: %w", err)
	}
	absPath := filepath.Clean(filepath.Join(absWorktree, filePath))
	if !strings.HasPrefix(absPath, absWorktree+string(filepath.Separator)) && absPath != absWorktree {
		return fmt.Errorf("path traversal denied")
	}

	// Try git checkout first (works for tracked files)
	cmd := exec.Command("git", "-C", worktreePath, "checkout", "--", filePath)
	if _, err := cmd.CombinedOutput(); err != nil {
		// If checkout fails, it might be an untracked file — remove it
		if _, statErr := os.Stat(absPath); statErr == nil {
			return os.Remove(absPath)
		}
		return fmt.Errorf("git checkout file: %w", err)
	}
	return nil
}

// CommitAll stages all changes and creates a commit with the given message.
func CommitAll(worktreePath, message string) error {
	// Stage all changes
	addCmd := exec.Command("git", "-C", worktreePath, "add", "-A")
	if out, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// Create commit
	commitCmd := exec.Command("git", "-C", worktreePath, "commit", "-m", message)
	if out, err := commitCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %s: %w", strings.TrimSpace(string(out)), err)
	}

	return nil
}

// Pull fetches and merges changes from the remote repository.
func Pull(worktreePath string) error {
	cmd := exec.Command("git", "-C", worktreePath, "pull")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git pull: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// Push pushes local changes to the remote repository.
func Push(worktreePath string) error {
	cmd := exec.Command("git", "-C", worktreePath, "push")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git push: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// GetCommitCount returns the total number of commits in the repository.
func GetCommitCount(repoPath string) (int, error) {
	cmd := exec.Command("git", "-C", repoPath, "rev-list", "--count", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("git rev-list count: %w", err)
	}
	var count int
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &count)
	return count, nil
}

// GetContributorCount returns the number of unique contributors.
func GetContributorCount(repoPath string) (int, error) {
	cmd := exec.Command("git", "-C", repoPath, "shortlog", "-sn", "--no-merges")
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("git shortlog: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	count := 0
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count, nil
}

// GetLastCommitDate returns the date of the most recent commit.
func GetLastCommitDate(repoPath string) (string, error) {
	cmd := exec.Command("git", "-C", repoPath, "log", "-1", "--format=%ai")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git log: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Fetch fetches remote branches without merging.
func Fetch(worktreePath string) error {
	cmd := exec.Command("git", "-C", worktreePath, "fetch", "--all")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git fetch: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// Merge merges the source branch into the target branch without switching branches.
// Strategy: fast-forward if possible, otherwise create a merge commit using
// git merge-tree (requires Git 2.38+) to avoid branch checkout in worktrees.
func Merge(worktreePath, sourceBranch, targetBranch string) error {
	// Resolve both branches to commit hashes
	sourceHash, err := resolveRef(worktreePath, sourceBranch)
	if err != nil {
		return fmt.Errorf("merge %s into %s: cannot resolve source: %w", sourceBranch, targetBranch, err)
	}
	targetHash, err := resolveRef(worktreePath, targetBranch)
	if err != nil {
		return fmt.Errorf("merge %s into %s: cannot resolve target: %w", sourceBranch, targetBranch, err)
	}

	// Already up-to-date?
	if sourceHash == targetHash {
		return nil
	}

	// Try fast-forward: target must be an ancestor of source
	isAncestor := exec.Command("git", "-C", worktreePath, "merge-base", "--is-ancestor", targetHash, sourceHash)
	if isAncestor.Run() == nil {
		// Fast-forward: just move the target ref
		return updateBranchRef(worktreePath, targetBranch, sourceHash, sourceBranch)
	}

	// Not fast-forward — create a real merge commit using merge-tree
	mergeTreeCmd := exec.Command("git", "-C", worktreePath, "merge-tree", "--write-tree", targetHash, sourceHash)
	mtOut, mtErr := mergeTreeCmd.CombinedOutput()
	if mtErr != nil {
		output := strings.TrimSpace(string(mtOut))
		if strings.Contains(output, "CONFLICT") {
			return fmt.Errorf("merge %s into %s: conflicts detected:\n%s", sourceBranch, targetBranch, output)
		}
		return fmt.Errorf("merge %s into %s: merge-tree failed: %s: %w", sourceBranch, targetBranch, output, mtErr)
	}

	treeHash := strings.TrimSpace(strings.SplitN(string(mtOut), "\n", 2)[0])

	// Create a merge commit with two parents
	msg := fmt.Sprintf("Merge branch '%s' into %s", sourceBranch, targetBranch)
	commitTreeCmd := exec.Command("git", "-C", worktreePath,
		"commit-tree", treeHash,
		"-p", targetHash, "-p", sourceHash,
		"-m", msg)
	ctOut, ctErr := commitTreeCmd.Output()
	if ctErr != nil {
		return fmt.Errorf("merge %s into %s: commit-tree failed: %w", sourceBranch, targetBranch, ctErr)
	}
	mergeCommitHash := strings.TrimSpace(string(ctOut))

	return updateBranchRef(worktreePath, targetBranch, mergeCommitHash, sourceBranch)
}

func resolveRef(worktreePath, ref string) (string, error) {
	cmd := exec.Command("git", "-C", worktreePath, "rev-parse", "--verify", ref)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func updateBranchRef(worktreePath, branch, commitHash, sourceBranch string) error {
	cmd := exec.Command("git", "-C", worktreePath, "update-ref", "refs/heads/"+branch, commitHash)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("merge %s into %s: update-ref: %s: %w", sourceBranch, branch, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// GenerateCommitMessage creates a conventional commit message by analyzing git status.
func GenerateCommitMessage(worktreePath string) (string, error) {
	statuses, err := Status(worktreePath)
	if err != nil {
		return "", err
	}
	if len(statuses) == 0 {
		return "", fmt.Errorf("no changes to commit")
	}

	var modified, added, deleted int
	for _, s := range statuses {
		switch {
		case s.Status == "M" || s.Status == "MM" || s.Status == "AM":
			modified++
		case s.Status == "A" || s.Status == "??" || s.Status == "UU":
			added++
		case s.Status == "D":
			deleted++
		default:
			modified++
		}
	}

	commitType := "chore"
	if added > modified && added > deleted {
		commitType = "feat"
	} else if deleted > modified && deleted > added {
		commitType = "refactor"
	} else if modified > 0 {
		commitType = "fix"
	}

	parts := make([]string, 0, 3)
	if modified > 0 {
		parts = append(parts, fmt.Sprintf("%d modified", modified))
	}
	if added > 0 {
		parts = append(parts, fmt.Sprintf("%d added", added))
	}
	if deleted > 0 {
		parts = append(parts, fmt.Sprintf("%d deleted", deleted))
	}

	message := fmt.Sprintf("%s: update %d files (%s)", commitType, len(statuses), strings.Join(parts, ", "))
	return message, nil
}

// PushSetUpstream pushes the current branch and sets upstream tracking.
func PushSetUpstream(worktreePath string) error {
	branch, err := CurrentBranch(worktreePath)
	if err != nil {
		return err
	}
	cmd := exec.Command("git", "-C", worktreePath, "push", "-u", "origin", "--", branch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git push -u origin %s: %s: %w", branch, strings.TrimSpace(string(out)), err)
	}
	return nil
}
