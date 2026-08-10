package api

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	stdpath "path"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/git"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/terminal"
)

type RepositoryHandler struct {
	svc   *service.RepositoryService
	Authz *service.Authz
	DB    *sql.DB // used for batch owner-name lookup on list endpoints
}

func NewRepositoryHandler(svc *service.RepositoryService) *RepositoryHandler {
	return &RepositoryHandler{svc: svc}
}

type CreateRepositoryRequest struct {
	Path      string `json:"path"`
	RemoteURL string `json:"remote_url"`
	Name      string `json:"name"`
	AutoInit  *bool  `json:"auto_init"` // Pointer to distinguish between unset and false
}

type UpdateRepositoryRequest struct {
	Name          string `json:"name" binding:"required"`
	Path          string `json:"path" binding:"required"`
	GitRemote     string `json:"git_remote"`
	DefaultBranch string `json:"default_branch"`
}

// List returns all repositories
// @Summary      List repositories
// @Description  Get all repositories
// @Tags         Repositories
// @Accept       json
// @Produce      json
// @Success      200  {array}   RepositoryResponse
// @Failure      500  {object}  Error
// @Router       /repositories [get]
// branchCount caches per-repository branch counts for the list endpoint. The
// count comes from a `git branch` subprocess per repository; without this the
// list spawned one unbounded git process per repo on EVERY request, which made
// the repositories list slow. Branch counts change rarely, so a short TTL keeps
// repeated navigations instant while staying near-real-time.
type branchCountEntry struct {
	count    int
	computed time.Time
}

var (
	branchCountMu    sync.Mutex
	branchCountCache = map[string]branchCountEntry{}
)

const branchCountTTL = 30 * time.Second

func cachedBranchCount(path string, now time.Time) (int, bool) {
	branchCountMu.Lock()
	defer branchCountMu.Unlock()
	e, ok := branchCountCache[path]
	if !ok || now.Sub(e.computed) > branchCountTTL {
		return 0, false
	}
	return e.count, true
}

func setBranchCount(path string, count int, now time.Time) {
	branchCountMu.Lock()
	defer branchCountMu.Unlock()
	branchCountCache[path] = branchCountEntry{count: count, computed: now}
}

// repoBranchConcurrency bounds the git-subprocess fan-out for branch counts.
func repoBranchConcurrency() int {
	n := runtime.NumCPU() * 2
	if n < 4 {
		return 4
	}
	if n > 16 {
		return 16
	}
	return n
}

func (h *RepositoryHandler) List(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	ctx := c.Request.Context()

	// Optional ?owner=user:<id>|org:<slug> filter — used by the org detail
	// "资源" tab to count repositories belonging to a specific org.
	ownerF, err := ParseOwnerFilter(c, h.DB)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}

	repositories, err := h.svc.List(ctx)
	if userID > 0 {
		repositories, err = h.svc.ListForUser(ctx, userID)
	}
	if err != nil {
		InternalError(c, err)
		return
	}
	if ownerF.Type != "" {
		filtered := repositories[:0]
		for _, r := range repositories {
			if ownerF.Match(r.OwnerType, r.OwnerID) {
				filtered = append(filtered, r)
			}
		}
		repositories = filtered
	}

	// Build owner lookup once for the whole page (two batch queries max).
	var responses []RepositoryResponse
	if h.DB != nil {
		refs := make([]ownerRef, len(repositories))
		for i, r := range repositories {
			refs[i] = ownerRef{r.OwnerType, r.OwnerID}
		}
		lk, _ := newOwnerLookup(ctx, h.DB, refs)
		responses = ToRepositoryResponsesWithLookup(repositories, lk)
	} else {
		responses = ToRepositoryResponses(repositories)
	}

	// Fill per-repo branch counts, serving from the TTL cache when fresh and
	// otherwise computing via `git branch` under a bounded fan-out (was
	// unbounded goroutines + no cache — the cause of the slow list).
	now := time.Now()
	sem := make(chan struct{}, repoBranchConcurrency())
	var wg sync.WaitGroup
	for i, repo := range repositories {
		if cnt, ok := cachedBranchCount(repo.Path, now); ok {
			responses[i].TotalBranches = cnt
			continue
		}
		wg.Add(1)
		go func(idx int, path string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			branches, berr := git.ListBranches(path)
			if berr == nil {
				responses[idx].TotalBranches = len(branches)
				setBranchCount(path, len(branches), time.Now())
			}
		}(i, repo.Path)
	}
	wg.Wait()
	c.JSON(http.StatusOK, responses)
}

// Get retrieves a repository by ID
// @Summary      Get a repository
// @Description  Get a repository by ID
// @Tags         Repositories
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Repository ID"
// @Success      200  {object}  RepositoryResponse
// @Failure      404  {object}  Error
// @Failure      500  {object}  Error
// @Router       /repositories/{id} [get]
func (h *RepositoryHandler) Get(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	if userID > 0 && h.Authz != nil {
		if repoID, perr := strconv.ParseInt(c.Param("id"), 10, 64); perr == nil {
			if _, aerr := h.Authz.CanAccessRepository(c.Request.Context(), userID, repoID); aerr != nil {
				writeAuthzError(c, aerr)
				return
			}
		}
	}
	repository, err := h.svc.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		slog.Warn("GetRepository failed", "id", c.Param("id"), "error", err)
		NotFound(c, "REPOSITORY")
		return
	}
	c.JSON(http.StatusOK, toRepositoryResponse(repository))
}

// Create creates a new repository
// @Summary      Create a repository
// @Description  Create a new repository. Provide either `path` (local directory) or `remote_url` (git remote).
// @Description  When `remote_url` is set, the repository is cloned into ~/git-repos/<name> first,
// @Description  then the cloned path is registered. Supports https://, http://, git://, ssh://,
// @Description  ftp://, ftps://, file://, and SCP-style git@ URLs.
// @Description  When `path` is provided and `auto_init` is true, the directory is created and git-init'd if needed.
// @Tags         Repositories
// @Accept       json
// @Produce      json
// @Param        request  body      CreateRepositoryRequest  true  "Repository details; provide path OR remote_url"
// @Success      201     {object}  RepositoryResponse
// @Failure      400     {object}  Error
// @Failure      409     {object}  Error
// @Failure      500     {object}  Error
// @Router       /repositories [post]
func (h *RepositoryHandler) Create(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	var req struct {
		Path      string `json:"path"`
		RemoteURL string `json:"remote_url"`
		Name      string `json:"name"`
		AutoInit  *bool  `json:"auto_init"`
		Owner     *struct {
			Type string `json:"type"`
			ID   int64  `json:"id"`
		} `json:"owner,omitempty"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}

	// Require at least one of path or remote_url
	if strings.TrimSpace(req.Path) == "" && strings.TrimSpace(req.RemoteURL) == "" {
		BadRequest(c, "path or remote_url is required")
		return
	}

	// Resolve owner: nil pointer OR sentinel {user,0} (personal-edition SPA
	// no-currentUser fallback) both default to the calling user's personal
	// space. See project.go Create for context.
	var owner service.OwnerRef
	if req.Owner != nil && req.Owner.Type != "" && !(req.Owner.Type == "user" && req.Owner.ID == 0) {
		owner = service.OwnerRef{Type: req.Owner.Type, ID: req.Owner.ID}
	} else {
		owner = service.OwnerRef{Type: "user", ID: userID}
	}
	if userID > 0 && h.Authz != nil {
		if err := h.Authz.EnsureOwnerWritable(c.Request.Context(), userID, owner); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	// Default auto_init to true if not specified (only meaningful for local paths)
	autoInit := true
	if req.AutoInit != nil {
		autoInit = *req.AutoInit
	}

	repository, warnings, err := h.svc.Create(c.Request.Context(), service.CreateRepositoryInput{
		Path:      req.Path,
		RemoteURL: req.RemoteURL,
		Name:      req.Name,
		AutoInit:  autoInit,
		OwnerType: owner.Type,
		OwnerID:   owner.ID,
	})
	if err != nil {
		switch {
		case containsPrefix(err.Error(), "CLONE_FAILED"):
			BadRequest(c, err.Error())
		case containsPrefix(err.Error(), "PATH_CREATION_FAILED"):
			BadRequest(c, err.Error())
		case containsPrefix(err.Error(), "PATH_DOES_NOT_EXIST"):
			BadRequest(c, err.Error())
		case containsPrefix(err.Error(), "NOT_A_DIRECTORY"):
			BadRequest(c, err.Error())
		case containsPrefix(err.Error(), "NOT_A_GIT_REPO"):
			BadRequest(c, err.Error())
		case containsPrefix(err.Error(), "REPO_NAME_EXISTS"):
			RespondError(c, http.StatusConflict, "REPO_NAME_EXISTS", err.Error()[len("REPO_NAME_EXISTS:"):])
		case containsPrefix(err.Error(), "GIT_IDENTITY_MISSING"):
			RespondError(c, http.StatusBadRequest, "GIT_IDENTITY_MISSING", err.Error())
		case containsPrefix(err.Error(), "GIT_INITIAL_COMMIT_FAILED"):
			RespondError(c, http.StatusInternalServerError, "GIT_INITIAL_COMMIT_FAILED", err.Error())
		case containsPrefix(err.Error(), "GIT_INIT_FAILED"):
			InternalError(c, err)
		case containsPrefix(err.Error(), "DB_WRITE_FAILED"):
			InternalError(c, err)
		default:
			InternalError(c, err)
		}
		return
	}

	resp := toRepositoryResponse(repository)
	resp.Warnings = warnings
	c.JSON(http.StatusCreated, resp)
}

func containsPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// Update updates an existing repository
// @Summary      Update a repository
// @Description  Update repository details
// @Tags         Repositories
// @Accept       json
// @Produce      json
// @Param        id       path      string                  true  "Repository ID"
// @Param        request  body      UpdateRepositoryRequest  true  "Updated repository details"
// @Success      200      {object}  RepositoryResponse
// @Failure      400      {object}  Error
// @Failure      404      {object}  Error
// @Failure      500      {object}  Error
// @Router       /repositories/{id} [put]
func (h *RepositoryHandler) Update(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	if userID > 0 && h.Authz != nil {
		if repoID, perr := strconv.ParseInt(c.Param("id"), 10, 64); perr == nil {
			if _, aerr := h.Authz.CanAccessRepository(c.Request.Context(), userID, repoID); aerr != nil {
				writeAuthzError(c, aerr)
				return
			}
		}
	}
	var req UpdateRepositoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}

	repository, err := h.svc.Update(c.Request.Context(), c.Param("id"), service.UpdateRepositoryInput{
		Name:          req.Name,
		Path:          req.Path,
		GitRemote:     req.GitRemote,
		DefaultBranch: req.DefaultBranch,
	})
	if err != nil {
		slog.Warn("UpdateRepository failed", "id", c.Param("id"), "error", err)
		NotFound(c, "REPOSITORY")
		return
	}

	c.JSON(http.StatusOK, toRepositoryResponse(repository))
}

// Delete removes a repository
// @Summary      Delete a repository
// @Description  Delete a repository. If delete_directory is true, also removes the directory from disk.
// @Tags         Repositories
// @Accept       json
// @Produce      json
// @Param        id               path      string  true   "Repository ID"
// @Param        delete_directory query     bool    false  "Also delete the repository directory from disk"
// @Success      204
// @Failure      500  {object}  Error
// @Router       /repositories/{id} [delete]
func (h *RepositoryHandler) Delete(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	if userID > 0 && h.Authz != nil {
		if repoID, perr := strconv.ParseInt(c.Param("id"), 10, 64); perr == nil {
			if _, aerr := h.Authz.CanAccessRepository(c.Request.Context(), userID, repoID); aerr != nil {
				writeAuthzError(c, aerr)
				return
			}
		}
	}
	deleteDir := c.Query("delete_directory") == "true"

	// Get repository path before deleting (to delete directory later if needed)
	if deleteDir {
		repo, err := h.svc.Get(c.Request.Context(), c.Param("id"))
		if err != nil {
			slog.Warn("DeleteRepository failed to get repo", "id", c.Param("id"), "error", err)
			NotFound(c, "REPOSITORY")
			return
		}
		// Delete from DB first
		if err := h.svc.Delete(c.Request.Context(), c.Param("id")); err != nil {
			InternalError(c, err)
			return
		}
		// Then delete directory from disk
		if err := h.svc.DeleteDirectory(repo.Path); err != nil {
			// Log but don't fail - DB record is already deleted
			slog.Error("failed to delete repository directory", "path", repo.Path, "error", err)
		}
	} else {
		if err := h.svc.Delete(c.Request.Context(), c.Param("id")); err != nil {
			InternalError(c, err)
			return
		}
	}
	c.Status(http.StatusNoContent)
}

// GetBranches returns all branches for a repository
// @Summary      Get repository branches
// @Description  Get all branches for a repository by its ID
// @Tags         Repositories
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Repository ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      404  {object}  Error
// @Failure      500  {object}  Error
// @Router       /repositories/{id}/branches [get]
func (h *RepositoryHandler) GetBranches(c *gin.Context) {
	result, err := h.svc.GetBranchInfo(c.Request.Context(), c.Param("id"))
	if err != nil {
		slog.Warn("GetBranches failed", "id", c.Param("id"), "error", err)
		NotFound(c, "REPOSITORY")
		return
	}
	c.JSON(http.StatusOK, result)
}

// GetStats returns repository statistics
// @Summary      Get repository stats
// @Description  Get statistics for a repository
// @Tags         Repositories
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Repository ID"
// @Success      200  {object}  service.RepositoryStats
// @Failure      404  {object}  Error
// @Failure      500  {object}  Error
// @Router       /repositories/{id}/stats [get]
func (h *RepositoryHandler) GetStats(c *gin.Context) {
	stats, err := h.svc.GetStats(c.Request.Context(), c.Param("id"))
	if err != nil {
		slog.Warn("GetStats failed", "id", c.Param("id"), "error", err)
		NotFound(c, "REPOSITORY")
		return
	}
	c.JSON(http.StatusOK, stats)
}

// ListFiles returns file entries in the repository
// @Summary      List repository files
// @Description  Get file tree at a given path in the repository
// @Tags         Repositories
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Repository ID"
// @Param        path query     string  false "Directory path"
// @Success      200  {array}   git.FileEntry
// @Failure      404  {object}  Error
// @Failure      500  {object}  Error
// @Router       /repositories/{id}/files [get]
func (h *RepositoryHandler) ListFiles(c *gin.Context) {
	path := c.Query("path")
	files, err := h.svc.ListFiles(c.Request.Context(), c.Param("id"), path)
	if err != nil {
		slog.Warn("ListFiles failed", "id", c.Param("id"), "path", path, "error", err)
		NotFound(c, "REPOSITORY")
		return
	}
	c.JSON(http.StatusOK, files)
}

// GetFileContent returns the content of a file
// @Summary      Get file content
// @Description  Get the content of a file at the given path
// @Tags         Repositories
// @Accept       json
// @Produce      json
// @Param        id       path      string  true  "Repository ID"
// @Param        path     query     string  true  "File path"
// @Success      200      {object}  map[string]interface{}
// @Failure      404      {object}  Error
// @Failure      500      {object}  Error
// @Router       /repositories/{id}/files/content [get]
func (h *RepositoryHandler) GetFileContent(c *gin.Context) {
	rawPath := c.Query("path")
	if rawPath == "" {
		BadRequest(c, "path is required")
		return
	}
	// Anchor at the tree root and collapse any traversal. stdpath.Clean keeps
	// forward slashes (git tree paths use '/'), unlike filepath.Clean on Windows.
	rel := strings.TrimPrefix(stdpath.Clean("/"+rawPath), "/")
	if rel == "" {
		BadRequest(c, "invalid path")
		return
	}
	ctx := c.Request.Context()
	id := c.Param("id")

	// Raw mode streams the file bytes with a real Content-Type so the shared
	// FilePreview component can render images / video / office documents from a
	// repository, mirroring the workspace file-content endpoint.
	if c.Query("mode") == "raw" {
		data, err := h.svc.GetFileBytes(ctx, id, rel)
		if err != nil {
			slog.Warn("GetFileContent raw failed", "id", id, "path", rel, "error", err)
			NotFound(c, "REPOSITORY")
			return
		}
		c.Header("Cache-Control", "private, max-age=3600")
		// Agent/user-authored HTML must never execute on the app origin (it could
		// read the SPA's stored token). Sandbox it to an opaque origin and disable
		// sniffing — same posture as the workspace raw endpoint.
		if ext := strings.ToLower(stdpath.Ext(rel)); ext == ".html" || ext == ".htm" {
			c.Header("Content-Security-Policy", "sandbox allow-scripts")
			c.Header("X-Content-Type-Options", "nosniff")
		}
		c.Data(http.StatusOK, detectContentType(rel), data)
		return
	}

	content, err := h.svc.GetFileContent(ctx, id, rel)
	if err != nil {
		slog.Warn("GetFileContent failed", "id", id, "path", rel, "error", err)
		NotFound(c, "REPOSITORY")
		return
	}
	c.JSON(http.StatusOK, gin.H{"content": content})
}

// ListCommits returns commit history
// @Summary      List repository commits
// @Description  Get commit history with pagination
// @Tags         Repositories
// @Accept       json
// @Produce      json
// @Param        id     path      string  true  "Repository ID"
// @Param        page   query     int     false "Page number (default 1)"
// @Param        limit  query     int     false "Items per page (default 20)"
// @Success      200    {array}   git.LogEntry
// @Failure      404    {object}  Error
// @Failure      500    {object}  Error
// @Router       /repositories/{id}/commits [get]
func (h *RepositoryHandler) ListCommits(c *gin.Context) {
	var page, limit int
	if p := c.Query("page"); p != "" {
		fmt.Sscanf(p, "%d", &page)
	} else {
		page = 1
	}
	if l := c.Query("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	} else {
		limit = 20
	}
	commits, err := h.svc.ListCommits(c.Request.Context(), c.Param("id"), page, limit)
	if err != nil {
		slog.Warn("ListCommits failed", "id", c.Param("id"), "page", page, "limit", limit, "error", err)
		NotFound(c, "REPOSITORY")
		return
	}
	c.JSON(http.StatusOK, commits)
}

// CreateBranch creates a new branch
// @Summary      Create branch
// @Description  Create a new branch in the repository
// @Tags         Repositories
// @Accept       json
// @Produce      json
// @Param        id       path      string  true  "Repository ID"
// @Param        request  body      map[string]interface{}  true  "Branch name"
// @Success      201
// @Failure      400      {object}  Error
// @Failure      404      {object}  Error
// @Failure      500      {object}  Error
// @Router       /repositories/{id}/branches [post]
func (h *RepositoryHandler) CreateBranch(c *gin.Context) {
	var req struct {
		Name string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if err := h.svc.CreateBranch(c.Request.Context(), c.Param("id"), req.Name); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusCreated)
}

// DeleteBranch deletes a branch
// @Summary      Delete branch
// @Description  Delete a branch from the repository. Branch name passed as query param to support slashes.
// @Tags         Repositories
// @Param        id       path      string  true  "Repository ID"
// @Param        name     query     string  true  "Branch name"
// @Success      204
// @Failure      400      {object}  Error
// @Failure      500      {object}  Error
// @Router       /repositories/{id}/branches [delete]
func (h *RepositoryHandler) DeleteBranch(c *gin.Context) {
	name := c.Query("name")
	if name == "" {
		BadRequest(c, "branch name is required")
		return
	}
	if err := h.svc.DeleteBranch(c.Request.Context(), c.Param("id"), name); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// CheckoutBranch switches to a branch
// @Summary      Checkout branch
// @Description  Switch to a different branch. Branch name passed as query param to support slashes.
// @Tags         Repositories
// @Param        id       path      string  true  "Repository ID"
// @Param        name     query     string  true  "Branch name"
// @Success      200
// @Failure      400      {object}  Error
// @Failure      500      {object}  Error
// @Router       /repositories/{id}/branches/checkout [put]
func (h *RepositoryHandler) CheckoutBranch(c *gin.Context) {
	name := c.Query("name")
	if name == "" {
		BadRequest(c, "branch name is required")
		return
	}
	if err := h.svc.CheckoutBranch(c.Request.Context(), c.Param("id"), name); err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.Status(http.StatusOK)
}

// ListWorktrees returns all worktrees
// @Summary      List worktrees
// @Description  Get all worktrees for a repository
// @Tags         Repositories
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Repository ID"
// @Success      200  {array}   git.WorktreeInfo
// @Failure      404  {object}  Error
// @Failure      500  {object}  Error
// @Router       /repositories/{id}/worktrees [get]
func (h *RepositoryHandler) ListWorktrees(c *gin.Context) {
	worktrees, err := h.svc.ListWorktrees(c.Request.Context(), c.Param("id"))
	if err != nil {
		slog.Warn("ListWorktrees failed", "id", c.Param("id"), "error", err)
		NotFound(c, "REPOSITORY")
		return
	}
	c.JSON(http.StatusOK, worktrees)
}

// CreateWorktree creates a new worktree
// @Summary      Create worktree
// @Description  Create a new worktree for the repository
// @Tags         Repositories
// @Accept       json
// @Produce      json
// @Param        id       path      string  true  "Repository ID"
// @Param        request  body      map[string]interface{}  true  "Worktree details"
// @Success      201
// @Failure      400      {object}  Error
// @Failure      404      {object}  Error
// @Failure      500      {object}  Error
// @Router       /repositories/{id}/worktrees [post]
func (h *RepositoryHandler) CreateWorktree(c *gin.Context) {
	var req struct {
		Path   string `json:"path" binding:"required"`
		Branch string `json:"branch" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if err := h.svc.CreateWorktree(c.Request.Context(), c.Param("id"), req.Path, req.Branch); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusCreated)
}

// RemoveWorktree removes a worktree
// @Summary      Remove worktree
// @Description  Remove a worktree by its database ID
// @Tags         Repositories
// @Accept       json
// @Produce      json
// @Param        id           path      string  true  "Repository ID"
// @Param        worktree_id  path      string  true  "Worktree ID"
// @Success      204
// @Failure      404      {object}  Error
// @Failure      500      {object}  Error
// @Router       /repositories/{id}/worktrees/{worktree_id} [delete]
func (h *RepositoryHandler) RemoveWorktree(c *gin.Context) {
	if err := h.svc.RemoveWorktree(c.Request.Context(), c.Param("id"), c.Param("worktree_id")); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// CommitAll stages and commits all changes
// @Summary      Commit all changes
// @Tags         Repositories
// @Param        id       path      string  true  "Repository ID"
// @Param        request  body      object  true  "Commit message"
// @Success      200
// @Failure      400      {object}  Error
// @Router       /repositories/{id}/commit [post]
func (h *RepositoryHandler) CommitAll(c *gin.Context) {
	var req struct {
		Message string `json:"message" binding:"required,max=10000"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if err := h.svc.CommitAll(c.Request.Context(), c.Param("id"), strings.TrimSpace(req.Message)); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusOK)
}

// DiscardAll discards all changes
// @Summary      Discard all changes
// @Tags         Repositories
// @Param        id   path      string  true  "Repository ID"
// @Success      200
// @Failure      500  {object}  Error
// @Router       /repositories/{id}/discard [post]
func (h *RepositoryHandler) DiscardAll(c *gin.Context) {
	if err := h.svc.DiscardAll(c.Request.Context(), c.Param("id")); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusOK)
}

// DiscardFile discards changes for a single file
// @Summary      Discard file changes
// @Tags         Repositories
// @Param        id   path      string  true  "Repository ID"
// @Success      200
// @Failure      400  {object}  Error
// @Router       /repositories/{id}/discard-file [post]
func (h *RepositoryHandler) DiscardFile(c *gin.Context) {
	var req struct {
		Path string `json:"path" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if err := h.svc.DiscardFile(c.Request.Context(), c.Param("id"), req.Path); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusOK)
}

// GetBranchTree returns local and remote branches for the sidebar tree
// @Summary      Get branch tree
// @Description  Get local and remote branches organized hierarchically
// @Tags         Repositories
// @Param        id   path      string  true  "Repository ID"
// @Success      200  {object}  service.BranchTree
// @Failure      404  {object}  Error
// @Router       /repositories/{id}/branch-tree [get]
func (h *RepositoryHandler) GetBranchTree(c *gin.Context) {
	tree, err := h.svc.GetBranchTree(c.Request.Context(), c.Param("id"))
	if err != nil {
		slog.Warn("GetBranchTree failed", "id", c.Param("id"), "error", err)
		NotFound(c, "REPOSITORY")
		return
	}
	c.JSON(http.StatusOK, tree)
}

// GetCommitDetail returns detail info for a single commit
// @Summary      Get commit detail
// @Description  Get detail info for a single commit including files changed
// @Tags         Repositories
// @Param        id     path      string  true  "Repository ID"
// @Param        hash   path      string  true  "Commit hash"
// @Success      200    {object}  git.CommitDetail
// @Failure      404    {object}  Error
// @Router       /repositories/{id}/commits/{hash} [get]
func (h *RepositoryHandler) GetCommitDetail(c *gin.Context) {
	detail, err := h.svc.GetCommitDetail(c.Request.Context(), c.Param("id"), c.Param("hash"))
	if err != nil {
		slog.Warn("GetCommitDetail failed", "id", c.Param("id"), "hash", c.Param("hash"), "error", err)
		NotFound(c, "REPOSITORY")
		return
	}
	c.JSON(http.StatusOK, detail)
}

// GetGraph returns commit graph data for branch visualization
// @Summary      Get branch graph
// @Description  Get commit graph data for branch visualization
// @Tags         Repositories
// @Accept       json
// @Produce      json
// @Param        id     path      string  true  "Repository ID"
// @Param        limit  query     int     false "Max commits; 0 or absent means ALL commits (graph computes the full DAG; the client virtualizes rendering)"
// @Param        all    query     bool    false "Include all branches (default true)"
// @Success      200    {array}   git.GraphCommit
// @Failure      404    {object}  Error
// @Router       /repositories/{id}/graph [get]
func (h *RepositoryHandler) GetGraph(c *gin.Context) {
	// limit=0 (or absent) means "all commits": the lanes are only topologically
	// correct when computed over the full DAG, and the SPA renders a virtualized
	// window so even 10k+ commits stay smooth. A positive limit is honored as an
	// explicit cap for callers that want a bounded fetch.
	var limit int
	if l := c.Query("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}
	if limit < 0 {
		limit = 0
	}
	allBranches := c.Query("all") != "false"

	commits, err := h.svc.GetGraphLog(c.Request.Context(), c.Param("id"), limit, allBranches)
	if err != nil {
		slog.Warn("GetGraph failed", "id", c.Param("id"), "error", err)
		NotFound(c, "REPOSITORY")
		return
	}
	c.JSON(http.StatusOK, commits)
}

// Terminal opens a WebSocket terminal session in the repository directory
// @Summary      Repository terminal
// @Description  Open a WebSocket terminal session in the repository directory
// @Tags         Repositories
// @Param        id   path      string  true  "Repository ID"
// @Router       /ws/repositories/{id}/terminal [get]
func (h *RepositoryHandler) Terminal(c *gin.Context) {
	repoPath, err := h.svc.GetPath(c.Request.Context(), c.Param("id"))
	if err != nil {
		slog.Warn("RepositoryTerminal: repo not found", "id", c.Param("id"), "error", err)
		NotFound(c, "REPOSITORY")
		return
	}

	if _, statErr := os.Stat(repoPath); os.IsNotExist(statErr) {
		BadRequest(c, "repository directory does not exist on disk")
		return
	}

	var shell string
	var args []string
	if runtime.GOOS == "windows" {
		shell = "cmd.exe"
		args = []string{}
	} else {
		shell = os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/bash"
		}
		args = []string{"-l"}
	}

	proc, err := terminal.NewPTYProcess(shell, args, repoPath, nil)
	if err != nil {
		InternalError(c, fmt.Errorf("create PTY process: %w", err))
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		proc.Close()
		InternalError(c, err)
		return
	}

	if err := terminal.Bridge(conn, proc, func() bool {
		return true
	}); err != nil {
		slog.Warn("repository terminal bridge error", "error", err)
	}
}

// GetDetail returns repository detail with git info and file tree
func (h *RepositoryHandler) GetDetail(c *gin.Context) {
	detail, err := h.svc.GetDetail(c.Request.Context(), c.Param("id"))
	if err != nil {
		if strings.Contains(err.Error(), "NOT_A_GIT_REPO") {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		slog.Warn("GetDetail failed", "id", c.Param("id"), "error", err)
		NotFound(c, "REPOSITORY")
		return
	}
	c.JSON(http.StatusOK, detail)
}
