package git

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"
)

// FileEntry represents a file or directory entry.
type FileEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"` // "file" or "dir"
	Size int64  `json:"size"`
	Mode string `json:"mode"`
}

// ListFiles returns file entries in a directory at the given treeish (branch/commit) and subpath.
func ListFiles(repoPath, treeish, subpath string) ([]FileEntry, error) {
	if treeish == "" {
		treeish = "HEAD"
	}
	// -c core.quotepath=false keeps non-ASCII paths as UTF-8 instead of octal-escaped C-quoted strings.
	// Using TREEISH:SUBPATH makes ls-tree enumerate the subpath's children rather than the tree entry itself.
	target := treeish
	if subpath != "" && subpath != "." {
		target = treeish + ":" + strings.TrimSuffix(subpath, "/")
	}
	args := []string{"-c", "core.quotepath=false", "-C", repoPath, "ls-tree", "--long", target}

	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	if err != nil {
		// exit status 128 typically means HEAD is invalid (empty repo with no commits)
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 128 {
			return []FileEntry{}, nil
		}
		return nil, fmt.Errorf("git ls-tree: %w", err)
	}

	output := strings.TrimSpace(string(out))
	if output == "" {
		return []FileEntry{}, nil
	}

	lines := strings.Split(output, "\n")
	entries := make([]FileEntry, 0, len(lines))

	for _, line := range lines {
		// --long format: mode type hash size\tname
		// Split on tab first to get name (which may contain spaces)
		tabParts := strings.SplitN(line, "\t", 2)
		if len(tabParts) < 2 {
			continue
		}
		name := tabParts[1]
		meta := strings.Fields(tabParts[0])
		if len(meta) < 4 {
			continue
		}

		mode := meta[0]
		objType := meta[1]

		// Determine type
		fileType := "file"
		if objType == "tree" {
			fileType = "dir"
		}

		// Size from --long output (meta[3])
		size := int64(0)
		if fileType == "file" {
			fmt.Sscanf(meta[3], "%d", &size)
		}

		entryPath := name
		if subpath != "" && subpath != "." {
			// Use path.Join (forward slashes) — these paths travel through JSON/URLs, not the OS fs.
			entryPath = path.Join(subpath, name)
		}

		entries = append(entries, FileEntry{
			Name: name,
			Path: entryPath,
			Type: fileType,
			Size: size,
			Mode: mode,
		})
	}

	// Dirs first, then files; each alphabetical.
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Type != entries[j].Type {
			return entries[i].Type == "dir"
		}
		return entries[i].Name < entries[j].Name
	})

	return entries, nil
}

// ListFilesLocal returns file entries in the working directory (not git tree).
func ListFilesLocal(dirPath string) ([]FileEntry, error) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, fmt.Errorf("read dir: %w", err)
	}

	result := make([]FileEntry, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		fileType := "file"
		if entry.IsDir() {
			fileType = "dir"
		}

		result = append(result, FileEntry{
			Name: entry.Name(),
			Path: entry.Name(),
			Type: fileType,
			Size: info.Size(),
			Mode: info.Mode().String(),
		})
	}

	return result, nil
}

// GetFileContent returns the content of a file at the given revision.
func GetFileContent(repoPath, treeish, filePath string) (string, error) {
	out, err := GetFileBytes(repoPath, treeish, filePath)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// GetFileBytes returns the raw bytes of a file blob at the given tree-ish.
// Unlike GetFileContent it preserves binary content unchanged, so it can back
// a raw media endpoint (images / video / office documents).
func GetFileBytes(repoPath, treeish, filePath string) ([]byte, error) {
	if treeish == "" {
		treeish = "HEAD"
	}
	args := []string{"-c", "core.quotepath=false", "-C", repoPath, "show", treeish + ":" + filePath}
	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git show: %w", err)
	}
	return out, nil
}

// GetFileContentLocal returns the content of a file from the working directory.
func GetFileContentLocal(filePath string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	return string(data), nil
}
