package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// repoSearchMaxFiles caps filename hits. The result list is flat, so a bare "e"
// over a large repo must not stream every path in the index.
const repoSearchMaxFiles = 100

// RepoSearchResponse is the unified answer for a repository search: filename
// hits and content hits in ONE payload.
//
// Both halves always come back, so the caller never has to pick a search mode up
// front — the same principle as the workspace search dialog binding both
// shortcuts to one panel. contentError is set (rather than failing the request)
// when the filename half succeeded but grep could not run, so a host without
// ripgrep degrades one half instead of the whole feature.
type RepoSearchResponse struct {
	Query          string                 `json:"query"`
	Files          []repoFileHit          `json:"files"`
	FilesTruncated bool                   `json:"filesTruncated"`
	Content        *ContentSearchResponse `json:"content,omitempty"`
	ContentError   string                 `json:"contentError,omitempty"`
}

// repoFileHit is one filename match, repo-relative with forward slashes.
type repoFileHit struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

// SearchRepository handles GET /repositories/:id/search?q=…
//
// Runs a filename search (git ls-files + fuzzy match) and a content search
// (ripgrep, falling back to git grep) over the repository, returning both.
// @Summary      Search a repository by filename and content
// @Tags         Repositories
// @Produce      json
// @Param        id    path   string true  "Repository ID"
// @Param        q     query  string true  "Search query"
// @Param        case  query  bool   false "Case-sensitive content match"
// @Param        word  query  bool   false "Whole-word content match"
// @Param        regex query  bool   false "Treat q as a regular expression"
// @Success      200   {object} RepoSearchResponse
// @Failure      400   {object} Error
// @Failure      404   {object} Error
// @Router       /repositories/{id}/search [get]
func (h *RepositoryHandler) SearchRepository(c *gin.Context) {
	opts, err := parseContentSearchOpts(c)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}

	repoPath, err := h.svc.GetPath(c.Request.Context(), c.Param("id"))
	if err != nil {
		NotFound(c, "REPOSITORY")
		return
	}
	// Resolve symlinks before handing the path to a search engine, mirroring the
	// workspace endpoint: the value comes from the DB, but the engines get a real
	// directory either way.
	root, err := resolveRepoDir(repoPath)
	if err != nil {
		NotFound(c, "REPOSITORY")
		return
	}

	resp := RepoSearchResponse{Query: opts.Query, Files: []repoFileHit{}}

	// --- filename half ---
	if entries, ferr := gitLsFiles(root, "", ""); ferr == nil {
		resp.Files, resp.FilesTruncated = matchFileNames(entries, opts.Query)
	}

	// --- content half ---
	// A grep failure is reported in-band: the filename half above is still
	// useful, and a hard failure here would hide it.
	content, cerr := runContentSearch(c.Request.Context(), root, opts)
	switch {
	case cerr == nil:
		resp.Content = content
	case errors.Is(cerr, errBadPattern):
		// A bad regex is the user's own input and invalidates the whole request.
		BadRequest(c, "invalid search pattern")
		return
	case errors.Is(cerr, errNoSearchEngine):
		resp.ContentError = "no_engine"
	default:
		resp.ContentError = "failed"
	}

	c.JSON(http.StatusOK, resp)
}

// matchFileNames filters entries by query and ranks them. Fuzzy matching is
// subsequence-based (as in the workspace file picker), so "wsp" still finds
// "workspace-page.tsx".
func matchFileNames(entries []fileEntry, query string) ([]repoFileHit, bool) {
	lowerQ := strings.ToLower(query)
	hits := make([]repoFileHit, 0, 16)
	for _, e := range entries {
		lowerPath := strings.ToLower(e.Path)
		if strings.Contains(lowerPath, lowerQ) || fuzzyMatch(lowerPath, lowerQ) {
			hits = append(hits, repoFileHit{Path: e.Path, Name: e.Name})
		}
	}
	// Substring matches rank above fuzzy-only ones, then shorter paths first —
	// the shortest containing path is nearly always the one that was meant.
	sort.SliceStable(hits, func(i, j int) bool {
		iSub := strings.Contains(strings.ToLower(hits[i].Path), lowerQ)
		jSub := strings.Contains(strings.ToLower(hits[j].Path), lowerQ)
		if iSub != jSub {
			return iSub
		}
		return len(hits[i].Path) < len(hits[j].Path)
	})
	if len(hits) > repoSearchMaxFiles {
		return hits[:repoSearchMaxFiles], true
	}
	return hits, false
}

// resolveRepoDir resolves symlinks and asserts the result is a directory.
func resolveRepoDir(path string) (string, error) {
	if path == "" {
		return "", errors.New("empty repository path")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("repository path is not a directory")
	}
	return abs, nil
}

// FileHistory handles GET /repositories/:id/files/history?path=…
// @Summary      Get a file's commit history
// @Tags         Repositories
// @Produce      json
// @Param        id    path   string true  "Repository ID"
// @Param        path  query  string true  "File path"
// @Param        limit query  int    false "Max commits (default 50, max 200)"
// @Success      200   {array} git.FileLogEntry
// @Failure      400   {object} Error
// @Failure      404   {object} Error
// @Router       /repositories/{id}/files/history [get]
func (h *RepositoryHandler) FileHistory(c *gin.Context) {
	path := c.Query("path")
	if path == "" {
		BadRequest(c, "path is required")
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))

	entries, err := h.svc.FileHistory(c.Request.Context(), c.Param("id"), path, limit)
	if err != nil {
		NotFound(c, "REPOSITORY")
		return
	}
	c.JSON(http.StatusOK, entries)
}

// FileDiffAtCommit handles GET /repositories/:id/files/history/:hash/diff?path=…
//
// `path` must be the name the file had AT that commit (FileLogEntry's
// path_at_commit), otherwise a pre-rename commit returns an empty patch.
// @Summary      Get the diff a commit introduced to one file
// @Tags         Repositories
// @Produce      json
// @Param        id    path   string true "Repository ID"
// @Param        hash  path   string true "Commit hash"
// @Param        path  query  string true "File path as of that commit"
// @Success      200   {object} map[string]string
// @Failure      400   {object} Error
// @Failure      404   {object} Error
// @Router       /repositories/{id}/files/history/{hash}/diff [get]
func (h *RepositoryHandler) FileDiffAtCommit(c *gin.Context) {
	path := c.Query("path")
	if path == "" {
		BadRequest(c, "path is required")
		return
	}
	patch, err := h.svc.FileDiffAtCommit(c.Request.Context(), c.Param("id"), c.Param("hash"), path)
	if err != nil {
		NotFound(c, "REPOSITORY")
		return
	}
	c.JSON(http.StatusOK, gin.H{"patch": patch})
}
