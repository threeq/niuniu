package git

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// WorktreeAdd creates a new worktree at targetPath on a new branch.
// When baseBranch is non-empty, the new branch is forked from that ref
// (passed as the <commit-ish> argument to `git worktree add`); otherwise
// the new branch is created from the repository's current HEAD.
func WorktreeAdd(repoPath, targetPath, branch, baseBranch string) error {
	args := []string{"-C", repoPath, "worktree", "add", "-b", branch, targetPath}
	if baseBranch != "" {
		args = append(args, baseBranch)
	}
	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree add: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// WorktreeRemove removes a worktree.
func WorktreeRemove(repoPath, targetPath string) error {
	cmd := exec.Command("git", "-C", repoPath, "worktree", "remove", "--force", targetPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree remove: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// WorktreePrune drops administrative entries for worktrees whose working
// directories have been deleted from disk (git worktree prune). Used after a
// user purge removes the user's directory tree, leaving stale worktree records
// in repos that live outside that tree (org / externally-registered repos).
func WorktreePrune(repoPath string) error {
	cmd := exec.Command("git", "-C", repoPath, "worktree", "prune")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree prune: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// sanitizePath replaces characters invalid in filesystem paths with hyphens.
// Handles Windows and Unix invalid characters: /, \, :, *, ?, ", <, >, |
func sanitizePath(s string) string {
	invalid := []string{"/", "\\", ":", "*", "?", "\"", "<", ">", "|"}
	result := s
	for _, char := range invalid {
		result = strings.ReplaceAll(result, char, "-")
	}
	return result
}

// WorktreeListBranches returns all local and remote branch names for a repository.
func WorktreeListBranches(repoPath string) ([]string, error) {
	cmd := exec.Command("git", "-C", repoPath, "branch", "-a", "--format=%(refname:short)")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git branch list: %s: %w", strings.TrimSpace(string(out)), err)
	}
	branches := strings.Split(strings.TrimSpace(string(out)), "\n")
	// Filter out empty strings
	result := make([]string, 0, len(branches))
	for _, b := range branches {
		if b != "" {
			result = append(result, b)
		}
	}
	return result, nil
}

// CreateAgentWorktree creates an isolated git worktree for an agent.
// repoWorktreePath: path to the main worktree (e.g., .worktrees/niuniu-main)
// repoName: canonical repo name from DB (e.g., "niuniu") — NOT parsed from path
// agentName: agent identifier (e.g., "coder-a")
// baseBranch: the branch to fork from (e.g., "ws-123/main")
// Returns: (agentWorktreePath, branchName, error)
func CreateAgentWorktree(repoWorktreePath, repoName, agentName, baseBranch string) (string, string, error) {
	parentDir := filepath.Dir(repoWorktreePath)
	agentDir := filepath.Join(parentDir, repoName+"--"+agentName)
	branchName := baseBranch + "/team/" + agentName

	cmd := exec.Command("git", "-C", repoWorktreePath, "worktree", "add",
		agentDir, "-b", branchName)
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("create agent worktree: %s: %w", string(output), err)
	}
	return agentDir, branchName, nil
}

// RemoveAgentWorktree removes an agent's isolated worktree.
func RemoveAgentWorktree(repoWorktreePath, agentWorktreePath string) error {
	cmd := exec.Command("git", "-C", repoWorktreePath, "worktree", "remove",
		agentWorktreePath, "--force")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("remove agent worktree: %s: %w", string(output), err)
	}
	return nil
}

// MergeBranch merges a branch into the current branch of the given worktree.
func MergeBranch(worktreePath, branchName string) error {
	cmd := exec.Command("git", "-C", worktreePath, "merge", branchName, "--no-edit")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("merge %s: %s: %w", branchName, string(output), err)
	}
	return nil
}

// MergeFastForwardOnly fast-forwards the worktree's current branch to branchName
// when possible. Unlike MergeBranch it NEVER creates a merge commit and NEVER
// leaves the worktree in a conflicted / MERGING state: when a fast-forward is not
// possible (the current branch has diverging commits) git refuses up front without
// touching the working tree or index, and this returns an error the caller can log
// and skip. Use it to sync an ancestor-tracking branch (e.g. the epic feature
// branch into the epic's own workspace) into a live worktree safely.
func MergeFastForwardOnly(worktreePath, branchName string) error {
	cmd := exec.Command("git", "-C", worktreePath, "merge", "--ff-only", branchName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ff-only merge %s: %s: %w", branchName, strings.TrimSpace(string(output)), err)
	}
	return nil
}

// WorktreeInfo represents a single worktree.
type WorktreeInfo struct {
	Path       string `json:"path"`
	Branch     string `json:"branch"`
	IsCurrent  bool   `json:"is_current"`
	HasChanges bool   `json:"has_changes"`
}

// ListWorktrees lists all worktrees for a repository.
func ListWorktrees(repoPath string) ([]WorktreeInfo, error) {
	cmd := exec.Command("git", "-C", repoPath, "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %w", err)
	}

	output := strings.TrimSpace(string(out))
	if output == "" {
		return []WorktreeInfo{}, nil
	}

	// Parse worktree list output
	lines := strings.Split(output, "\n")
	worktrees := make([]WorktreeInfo, 0)
	var current WorktreeInfo
	var inWorktree bool

	for _, line := range lines {
		if strings.HasPrefix(line, "worktree ") {
			if inWorktree && current.Path != "" {
				worktrees = append(worktrees, current)
			}
			current = WorktreeInfo{
				Path: strings.TrimPrefix(line, "worktree "),
			}
			inWorktree = true
		} else if inWorktree {
			if strings.HasPrefix(line, "branch ") {
				current.Branch = strings.TrimPrefix(line, "branch ")
			} else if line == "(detached)" {
				current.Branch = "HEAD"
			} else if strings.HasPrefix(line, "HEAD ") {
				current.IsCurrent = true
			}
		}
	}

	if inWorktree && current.Path != "" {
		worktrees = append(worktrees, current)
	}

	// Check for uncommitted changes in each worktree
	for i := range worktrees {
		status, _ := Status(worktrees[i].Path)
		worktrees[i].HasChanges = len(status) > 0
	}

	return worktrees, nil
}
