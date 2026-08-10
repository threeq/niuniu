package git

import (
	"fmt"
	"os/exec"
	"strings"
)

type GraphCommit struct {
	Hash      string   `json:"hash"`
	ShortHash string   `json:"short_hash"`
	Author    string   `json:"author"`
	Date      string   `json:"date"`
	Message   string   `json:"message"`
	Parents   []string `json:"parents"`
	Refs      []string `json:"refs,omitempty"`
	IsCurrent bool     `json:"is_current,omitempty"`
}

type CommitDetail struct {
	Hash         string       `json:"hash"`
	ShortHash    string       `json:"short_hash"`
	Author       string       `json:"author"`
	AuthorEmail  string       `json:"author_email"`
	Date         string       `json:"date"`
	Message      string       `json:"message"`
	FilesChanged []FileChange `json:"files_changed"`
}

type FileChange struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

func GetCommitDetail(repoPath, commitHash string) (*CommitDetail, error) {
	infoCmd := exec.Command("git", "-C", repoPath, "log", "-1",
		"--format=%H%x1e%h%x1e%an%x1e%ae%x1e%ai%x1e%B", commitHash)
	infoOut, err := infoCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log commit: %w", err)
	}
	parts := strings.SplitN(strings.TrimSpace(string(infoOut)), "\x1e", 6)
	if len(parts) != 6 {
		return nil, fmt.Errorf("unexpected git log output")
	}

	filesCmd := exec.Command("git", "-C", repoPath, "diff-tree", "--no-commit-id", "-r", "--name-status", commitHash)
	filesOut, err := filesCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff-tree: %w", err)
	}

	var files []FileChange
	for _, line := range strings.Split(strings.TrimSpace(string(filesOut)), "\n") {
		if line == "" {
			continue
		}
		fileParts := strings.SplitN(line, "\t", 2)
		if len(fileParts) == 2 {
			files = append(files, FileChange{Status: fileParts[0], Path: fileParts[1]})
		}
	}

	return &CommitDetail{
		Hash:         parts[0],
		ShortHash:    parts[1],
		Author:       parts[2],
		AuthorEmail:  parts[3],
		Date:         parts[4],
		Message:      strings.TrimSpace(parts[5]),
		FilesChanged: files,
	}, nil
}

func GraphLog(repoPath string, limit int, allBranches bool) ([]GraphCommit, error) {
	// Use ASCII record separator (\x1e) to avoid corruption from pipe characters in commit messages
	args := []string{"-C", repoPath, "log",
		"--format=%H%x1e%h%x1e%an%x1e%ai%x1e%s%x1e%P%x1e%D",
		"--topo-order",
	}
	if allBranches {
		args = append(args, "--all")
	}
	if limit > 0 {
		args = append(args, "-n", fmt.Sprintf("%d", limit))
	}

	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log graph: %w", err)
	}

	output := strings.TrimSpace(string(out))
	if output == "" {
		return []GraphCommit{}, nil
	}

	currentBranch, _ := CurrentBranch(repoPath)

	lines := strings.Split(output, "\n")
	commits := make([]GraphCommit, 0, len(lines))

	for _, line := range lines {
		parts := strings.SplitN(line, "\x1e", 7)
		if len(parts) != 7 {
			continue
		}

		var parents []string
		if parts[5] != "" {
			for _, p := range strings.Split(parts[5], " ") {
				if p = strings.TrimSpace(p); p != "" {
					parents = append(parents, p)
				}
			}
		}

		var refs []string
		isCurrent := false
		if parts[6] != "" {
			for _, r := range strings.Split(parts[6], ",") {
				r = strings.TrimSpace(r)
				if r == "" {
					continue
				}
				if branchName, found := strings.CutPrefix(r, "HEAD -> "); found {
					refs = append(refs, branchName)
					isCurrent = true
				} else if r == "HEAD" {
					isCurrent = true
				} else {
					refs = append(refs, r)
				}
			}
		}

		if !isCurrent && len(refs) > 0 {
			for _, ref := range refs {
				if ref == currentBranch {
					isCurrent = true
					break
				}
			}
		}

		commits = append(commits, GraphCommit{
			Hash:      parts[0],
			ShortHash: parts[1],
			Author:    parts[2],
			Date:      parts[3],
			Message:   parts[4],
			Parents:   parents,
			Refs:      refs,
			IsCurrent: isCurrent,
		})
	}

	return commits, nil
}
