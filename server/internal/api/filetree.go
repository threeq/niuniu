package api

import (
	"bufio"
	"bytes"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// FileTreeHandler handles file tree queries for workspaces.
type FileTreeHandler struct {
	queries *store.Queries
	Authz   *service.Authz
}

// NewFileTreeHandler creates a new FileTreeHandler.
func NewFileTreeHandler(queries *store.Queries) *FileTreeHandler {
	return &FileTreeHandler{queries: queries}
}

// fileEntry represents a single file or directory entry in the workspace tree.
type fileEntry struct {
	Path  string `json:"path"`  // full relative path from workspace root, e.g. ".worktrees/niuniu-main/src/app.tsx"
	Name  string `json:"name"`  // filename only
	Repo  string `json:"repo"`  // worktree/repo name, e.g. "niuniu-main"
	IsDir bool   `json:"isDir"`
}

// Search handles GET /workspaces/:id/files?q=search
// Returns up to 50 file entries filtered by an optional fuzzy query.
func (h *FileTreeHandler) Search(c *gin.Context) {
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}

	if userID := c.GetInt64("auth_user_id"); userID > 0 && h.Authz != nil {
		if _, aerr := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, workspaceID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}

	query := strings.ToLower(strings.TrimSpace(c.Query("q")))

	ctx := c.Request.Context()
	ws, err := h.queries.GetWorkspace(ctx, workspaceID)
	if err != nil {
		InternalError(c, fmt.Errorf("workspace not found: %w", err))
		return
	}

	files, err := listGitFiles(ws.Path)
	if err != nil {
		InternalError(c, fmt.Errorf("failed to list files: %w", err))
		return
	}

	// Filter by query using fuzzyMatch
	if query != "" {
		filtered := files[:0]
		for _, f := range files {
			if fuzzyMatch(strings.ToLower(f.Path), query) {
				filtered = append(filtered, f)
			}
		}
		files = filtered
	}

	// Sort: files with query match in name first, then alphabetical
	if query != "" {
		sort.SliceStable(files, func(i, j int) bool {
			iNameMatch := fuzzyMatch(strings.ToLower(files[i].Name), query)
			jNameMatch := fuzzyMatch(strings.ToLower(files[j].Name), query)
			if iNameMatch != jNameMatch {
				return iNameMatch
			}
			return files[i].Path < files[j].Path
		})
	} else {
		sort.Slice(files, func(i, j int) bool {
			return files[i].Path < files[j].Path
		})
	}

	// Limit to 50 results
	if len(files) > 50 {
		files = files[:50]
	}

	// A nil slice marshals to JSON null, which the SPA cannot consume; force [].
	if files == nil {
		files = []fileEntry{}
	}

	c.JSON(http.StatusOK, gin.H{"files": files})
}

// listGitFiles lists all files across all worktrees under wsPath/.worktrees/.
// Falls back to wsPath itself if no .worktrees/ directory exists.
func listGitFiles(wsPath string) ([]fileEntry, error) {
	worktreesDir := filepath.Join(wsPath, ".worktrees")
	info, err := os.Stat(worktreesDir)
	if err != nil || !info.IsDir() {
		// Fallback: treat wsPath as a single git repo
		entries, err := gitLsFiles(wsPath, "", "")
		if err != nil {
			// If git ls-files fails, fall back to filesystem walk
			return listFilesWalk(wsPath)
		}
		return entries, nil
	}

	// Enumerate subdirectories of .worktrees/
	subdirs, err := os.ReadDir(worktreesDir)
	if err != nil {
		return nil, fmt.Errorf("read .worktrees dir: %w", err)
	}

	var all []fileEntry
	foundAny := false
	for _, d := range subdirs {
		if !d.IsDir() {
			continue
		}
		repoName := d.Name()
		repoPath := filepath.Join(worktreesDir, repoName)
		pathPrefix := ".worktrees/" + repoName + "/"

		entries, err := gitLsFiles(repoPath, pathPrefix, repoName)
		if err != nil {
			// If git fails for this repo, fall back to walk
			walked, walkErr := listFilesWalkRepo(repoPath, pathPrefix, repoName)
			if walkErr == nil {
				all = append(all, walked...)
				foundAny = true
			}
			continue
		}
		all = append(all, entries...)
		foundAny = true
	}

	if !foundAny {
		return listFilesWalk(wsPath)
	}

	return all, nil
}

// gitLsFiles runs `git ls-files --cached --others --exclude-standard` in repoPath
// and returns fileEntry slices with the given pathPrefix and repoName.
func gitLsFiles(repoPath, pathPrefix, repoName string) ([]fileEntry, error) {
	// -c core.quotepath=false keeps non-ASCII paths as UTF-8 instead of octal-escaped C-quoted strings.
	cmd := exec.Command("git", "-c", "core.quotepath=false", "ls-files", "--cached", "--others", "--exclude-standard")
	cmd.Dir = repoPath

	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git ls-files in %s: %w (stderr: %s)", repoPath, err, stderr.String())
	}

	var entries []fileEntry
	scanner := bufio.NewScanner(&out)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fullPath := pathPrefix + line
		name := filepath.Base(line)
		entries = append(entries, fileEntry{
			Path:  fullPath,
			Name:  name,
			Repo:  repoName,
			IsDir: false,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanning git ls-files output: %w", err)
	}

	return entries, nil
}

// listFilesWalk is a fallback for non-git directories.
// It walks all .worktrees/ subdirs, skipping common build/cache directories.
func listFilesWalk(wsPath string) ([]fileEntry, error) {
	worktreesDir := filepath.Join(wsPath, ".worktrees")
	info, err := os.Stat(worktreesDir)
	if err != nil || !info.IsDir() {
		// Walk wsPath itself
		return listFilesWalkRepo(wsPath, "", "")
	}

	subdirs, err := os.ReadDir(worktreesDir)
	if err != nil {
		return nil, fmt.Errorf("read .worktrees dir: %w", err)
	}

	var all []fileEntry
	for _, d := range subdirs {
		if !d.IsDir() {
			continue
		}
		repoName := d.Name()
		repoPath := filepath.Join(worktreesDir, repoName)
		pathPrefix := ".worktrees/" + repoName + "/"
		entries, err := listFilesWalkRepo(repoPath, pathPrefix, repoName)
		if err != nil {
			continue
		}
		all = append(all, entries...)
	}

	return all, nil
}

// skipDirs contains directory names to skip during filesystem walk.
var skipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"dist":         true,
	".attachments": true,
	"__pycache__":  true,
}

// listFilesWalkRepo walks a single repo directory and returns fileEntry slices.
func listFilesWalkRepo(root, pathPrefix, repoName string) ([]fileEntry, error) {
	var entries []fileEntry

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}

		name := d.Name()

		// Skip hidden files/dirs at root level (e.g. .git)
		if skipDirs[name] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip the root itself
		if path == root {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}

		// Normalize to forward slashes
		rel = filepath.ToSlash(rel)
		fullPath := pathPrefix + rel

		entries = append(entries, fileEntry{
			Path:  fullPath,
			Name:  name,
			Repo:  repoName,
			IsDir: d.IsDir(),
		})

		return nil
	})

	if err != nil {
		return nil, err
	}

	return entries, nil
}

// fuzzyMatch returns true if all characters of query appear in s in order.
func fuzzyMatch(s, query string) bool {
	if query == "" {
		return true
	}
	qi := 0
	for _, ch := range s {
		if qi < len(query) && ch == rune(query[qi]) {
			qi++
		}
		if qi == len(query) {
			return true
		}
	}
	return qi == len(query)
}
