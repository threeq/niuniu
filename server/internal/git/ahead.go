package git

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// AheadCount returns the number of commits on currentBranch that are not on targetBranch.
// Returns 0 if either branch does not exist.
func AheadCount(repoPath, targetBranch, currentBranch string) (int, error) {
	cmd := exec.Command("git", "-C", repoPath, "rev-list", "--count", targetBranch+".."+currentBranch)
	out, err := cmd.Output()
	if err != nil {
		// Branch may not exist; treat as 0
		return 0, nil
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("parse ahead count: %w", err)
	}
	return count, nil
}

// DetectDefaultBranch returns the default branch name for a repository.
// Checks for "master" first, then "main". Falls back to "main" if neither exists.
func DetectDefaultBranch(repoPath string) string {
	for _, branch := range []string{"master", "main"} {
		cmd := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", "refs/heads/"+branch)
		if err := cmd.Run(); err == nil {
			return branch
		}
	}
	return "main"
}
