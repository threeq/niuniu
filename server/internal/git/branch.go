package git

import (
	"fmt"
	"os/exec"
	"strings"
)

// ListBranches returns a list of all branches in the repository.
func ListBranches(repoPath string) ([]string, error) {
	cmd := exec.Command("git", "-C", repoPath, "branch", "--list", "--format=%(refname:short)")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git branch list: %w", err)
	}

	branches := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(branches) == 1 && branches[0] == "" {
		return []string{}, nil
	}

	return branches, nil
}

// CurrentBranch returns the name of the current branch.
func CurrentBranch(repoPath string) (string, error) {
	cmd := exec.Command("git", "-C", repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git current branch: %w", err)
	}

	return strings.TrimSpace(string(out)), nil
}

// CreateBranch creates a new branch with the given name.
func CreateBranch(repoPath, branchName string) error {
	cmd := exec.Command("git", "-C", repoPath, "branch", "--", branchName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git branch create: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// CreateBranchFrom creates a new branch named name based on base (an existing
// branch or commit-ish), without switching the working tree. Equivalent to
// "git -C repoPath branch -- name base".
func CreateBranchFrom(repoPath, name, base string) error {
	cmd := exec.Command("git", "-C", repoPath, "branch", "--", name, base)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git branch create %s from %s: %s: %w", name, base, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// BranchExists reports whether a local branch with the given name exists.
func BranchExists(repoPath, branchName string) bool {
	cmd := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", "--quiet", "refs/heads/"+branchName)
	return cmd.Run() == nil
}

// BranchIsAncestor reports whether commit `ancestor` (branch or commit-ish) is
// already contained in `head` — i.e. "git merge-base --is-ancestor" exits 0.
// The epic merge-to-main flow uses it to skip the main→epic pre-merge when
// main has nothing new to contribute (merging anyway would move the epic ref
// to a redundant no-op merge commit). An unresolvable ref reports false so
// callers fall through to the merge path and surface its error.
func BranchIsAncestor(repoPath, ancestor, head string) bool {
	cmd := exec.Command("git", "-C", repoPath, "merge-base", "--is-ancestor", ancestor, head)
	return cmd.Run() == nil
}

// DeleteBranch deletes a branch.
func DeleteBranch(repoPath, branchName string) error {
	cmd := exec.Command("git", "-C", repoPath, "branch", "-D", "--", branchName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git branch delete: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// DeleteBranchMerged deletes a branch with git's SAFE delete (-d): the
// command refuses unless the branch is fully merged into the repo's HEAD, so
// a mid-flight integration branch survives an archive while a fully-merged
// one is cleaned up.
func DeleteBranchMerged(repoPath, branchName string) error {
	cmd := exec.Command("git", "-C", repoPath, "branch", "-d", "--", branchName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git branch safe delete: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// ListRemoteBranches returns remote branch names (e.g. "origin/main").
func ListRemoteBranches(repoPath string) ([]string, error) {
	cmd := exec.Command("git", "-C", repoPath, "branch", "-r", "--format=%(refname:short)")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git branch -r: %w", err)
	}
	output := strings.TrimSpace(string(out))
	if output == "" {
		return []string{}, nil
	}
	branches := strings.Split(output, "\n")
	result := make([]string, 0, len(branches))
	for _, b := range branches {
		b = strings.TrimSpace(b)
		if b != "" && !strings.Contains(b, "->") {
			result = append(result, b)
		}
	}
	return result, nil
}

// CheckoutBranch switches to the specified branch.
// Note: cannot use "--" separator with checkout for branches (git interprets it as file path).
// Instead, use "git switch" which is unambiguous for branch switching.
func CheckoutBranch(repoPath, branchName string) error {
	cmd := exec.Command("git", "-C", repoPath, "switch", branchName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git switch: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}
