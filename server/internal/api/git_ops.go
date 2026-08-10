package api

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

type GitOpsHandler struct {
	gitOpsSvc *service.GitOpsService
	Authz     *service.Authz
}

func NewGitOpsHandler(gitOpsSvc *service.GitOpsService) *GitOpsHandler {
	return &GitOpsHandler{gitOpsSvc: gitOpsSvc}
}

type GitStatusResponse struct {
	Modified  []string `json:"modified"`
	Added     []string `json:"added"`
	Deleted   []string `json:"deleted"`
	Untracked []string `json:"untracked"`
}

// GetStatus returns the git status for a repository.
// @Summary      Get git status
// @Description  Get the git status for a repository in a workspace
// @Tags         Git
// @Accept       json
// @Produce      json
// @Param        id        path      string  true  "Workspace ID"
// @Param        repo_id   path      string  true  "Repository ID"
// @Success      200  {object}  GitStatusResponse
// @Failure      404  {object}  Error
// @Failure      500  {object}  Error
// @Router       /workspaces/{id}/repositories/{repo_id}/git/status [get]
func (h *GitOpsHandler) GetStatus(c *gin.Context) {
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	repoID, err := strconv.ParseInt(c.Param("repo_id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid repository ID")
		return
	}

	status, err := h.gitOpsSvc.Status(c.Request.Context(), workspaceID, repoID)
	if err != nil {
		slog.Warn("GetStatus failed", "workspaceID", workspaceID, "repoID", repoID, "error", err)
		NotFound(c, "WORKSPACE_REPOSITORY")
		return
	}

	// Transform raw git status codes to normalized categories
	response := GitStatusResponse{
		Modified:  []string{},
		Added:     []string{},
		Deleted:   []string{},
		Untracked: []string{},
	}

	for _, f := range status {
		// Git porcelain format: XY PATH where X=index, Y=working tree
		// M = modified, A = added, D = deleted, ?? = untracked
		index := f.Status[0]
		working := f.Status[1]

		switch {
		case f.Status == "??": // untracked
			response.Untracked = append(response.Untracked, f.Path)
		case index == 'A': // added to index
			response.Added = append(response.Added, f.Path)
		case index == 'D': // deleted from index
			response.Deleted = append(response.Deleted, f.Path)
		case working == 'D': // deleted in working tree
			response.Deleted = append(response.Deleted, f.Path)
		case index == 'M' || working == 'M': // modified
			response.Modified = append(response.Modified, f.Path)
		default:
			// Treat any other change as modified
			response.Modified = append(response.Modified, f.Path)
		}
	}

	c.JSON(http.StatusOK, response)
}

// GetRepoStatus returns the git status for a repository.
// @Summary      Get repository git status
// @Description  Get the git status for a repository. Uses workspace worktree if workspace_id is provided, otherwise uses the repository's original path.
// @Tags         Git
// @Accept       json
// @Produce      json
// @Param        id           path  int  true  "Repository ID"
// @Param        workspace_id query int  false "Workspace ID (optional, for worktree path)"
// @Success      200  {object}  GitStatusResponse
// @Failure      400  {object}  Error
// @Failure      404  {object}  Error
// @Failure      500  {object}  Error
// @Router       /repositories/{id}/git/status [get]
func (h *GitOpsHandler) GetRepoStatus(c *gin.Context) {
	repoID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid repository ID")
		return
	}

	if userID := c.GetInt64("auth_user_id"); userID > 0 && h.Authz != nil {
		if _, aerr := h.Authz.CanAccessRepository(c.Request.Context(), userID, repoID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}

	// Parse optional workspace_id from query param
	var workspaceID int64
	if wsIDStr := c.Query("workspace_id"); wsIDStr != "" {
		workspaceID, err = strconv.ParseInt(wsIDStr, 10, 64)
		if err != nil {
			BadRequest(c, "invalid workspace ID")
			return
		}
	}

	status, err := h.gitOpsSvc.StatusByRepoID(c.Request.Context(), repoID, workspaceID)
	if err != nil {
		slog.Warn("GetRepoStatus failed", "repoID", repoID, "workspaceID", workspaceID, "error", err)
		NotFound(c, "REPOSITORY")
		return
	}

	// Transform raw git status codes to normalized categories (same as GetStatus)
	response := GitStatusResponse{
		Modified:  []string{},
		Added:     []string{},
		Deleted:   []string{},
		Untracked: []string{},
	}

	for _, f := range status {
		if len(f.Status) == 0 {
			continue
		}
		s := f.Status
		switch {
		case s == "??":
			response.Untracked = append(response.Untracked, f.Path)
		case s[0] == 'A':
			response.Added = append(response.Added, f.Path)
		case s[0] == 'D' || (len(s) > 1 && s[1] == 'D'):
			response.Deleted = append(response.Deleted, f.Path)
		case s[0] == 'M' || (len(s) > 1 && s[1] == 'M'):
			response.Modified = append(response.Modified, f.Path)
		default:
			response.Modified = append(response.Modified, f.Path)
		}
	}

	c.JSON(http.StatusOK, response)
}

// GetLog returns the commit history for a repository.
// @Summary      Get commit history
// @Description  Get the commit history for a repository in a workspace
// @Tags         Git
// @Accept       json
// @Produce      json
// @Param        id        path      string  true  "Workspace ID"
// @Param        repo_id   path      string  true  "Repository ID"
// @Param        limit     query     int     false "Limit number of commits (default: 10)"
// @Success      200  {object}  interface{}
// @Failure      404  {object}  Error
// @Failure      500  {object}  Error
// @Router       /workspaces/{id}/repositories/{repo_id}/git/log [get]
func (h *GitOpsHandler) GetLog(c *gin.Context) {
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	repoID, err := strconv.ParseInt(c.Param("repo_id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid repository ID")
		return
	}

	// Parse limit parameter
	limit := 10 // default
	if limitStr := c.Query("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	log, err := h.gitOpsSvc.Log(c.Request.Context(), workspaceID, repoID, limit)
	if err != nil {
		slog.Warn("GetLog failed", "workspaceID", workspaceID, "repoID", repoID, "error", err)
		NotFound(c, "WORKSPACE_REPOSITORY")
		return
	}

	c.JSON(http.StatusOK, log)
}

type CommitRequest struct {
	Message string `json:"message" binding:"required"`
}

// Commit creates a commit with all changes.
// @Summary      Create a commit
// @Description  Create a commit with all current changes
// @Tags         Git
// @Accept       json
// @Produce      json
// @Param        id        path      string          true  "Workspace ID"
// @Param        repo_id   path      string          true  "Repository ID"
// @Param        request   body      CommitRequest   true  "Commit details"
// @Success      204
// @Failure      400  {object}  Error
// @Failure      404  {object}  Error
// @Failure      500  {object}  Error
// @Router       /workspaces/{id}/repositories/{repo_id}/git/commit [post]
func (h *GitOpsHandler) Commit(c *gin.Context) {
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	repoID, err := strconv.ParseInt(c.Param("repo_id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid repository ID")
		return
	}

	var req CommitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}

	if err := h.gitOpsSvc.Commit(c.Request.Context(), workspaceID, repoID, req.Message); err != nil {
		InternalError(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}

// Pull fetches and merges changes from remote.
// @Summary      Pull changes
// @Description  Fetch and merge changes from remote
// @Tags         Git
// @Accept       json
// @Produce      json
// @Param        id        path      string  true  "Workspace ID"
// @Param        repo_id   path      string  true  "Repository ID"
// @Success      204
// @Failure      404  {object}  Error
// @Failure      500  {object}  Error
// @Router       /workspaces/{id}/repositories/{repo_id}/git/pull [post]
func (h *GitOpsHandler) Pull(c *gin.Context) {
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	repoID, err := strconv.ParseInt(c.Param("repo_id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid repository ID")
		return
	}

	if err := h.gitOpsSvc.Pull(c.Request.Context(), workspaceID, repoID); err != nil {
		InternalError(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}

// Push pushes local changes to remote.
// @Summary      Push changes
// @Description  Push local changes to remote
// @Tags         Git
// @Accept       json
// @Produce      json
// @Param        id        path      string  true  "Workspace ID"
// @Param        repo_id   path      string  true  "Repository ID"
// @Success      204
// @Failure      404  {object}  Error
// @Failure      500  {object}  Error
// @Router       /workspaces/{id}/repositories/{repo_id}/git/push [post]
func (h *GitOpsHandler) Push(c *gin.Context) {
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	repoID, err := strconv.ParseInt(c.Param("repo_id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid repository ID")
		return
	}

	if err := h.gitOpsSvc.Push(c.Request.Context(), workspaceID, repoID); err != nil {
		InternalError(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}

// Fetch fetches remote branches without merging.
// @Summary      Fetch branches
// @Description  Fetch remote branches without merging
// @Tags         Git
// @Accept       json
// @Produce      json
// @Param        id        path      string  true  "Workspace ID"
// @Param        repo_id   path      string  true  "Repository ID"
// @Success      204
// @Failure      404  {object}  Error
// @Failure      500  {object}  Error
// @Router       /workspaces/{id}/repositories/{repo_id}/git/fetch [post]
func (h *GitOpsHandler) Fetch(c *gin.Context) {
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	repoID, err := strconv.ParseInt(c.Param("repo_id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid repository ID")
		return
	}

	if err := h.gitOpsSvc.Fetch(c.Request.Context(), workspaceID, repoID); err != nil {
		InternalError(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}

type BranchesResponse struct {
	Branches      []string `json:"branches"`
	CurrentBranch string   `json:"current_branch"`
}

// GetFileContent returns the content of a file.
// @Summary      Get file content
// @Description  Get the content of a file from the repository HEAD
// @Tags         Git
// @Accept       json
// @Produce      json
// @Param        id        path      string  true  "Workspace ID"
// @Param        repo_id   path      string  true  "Repository ID"
// @Param        path      query     string  true  "File path"
// @Success      200  {object}  FileContentResponse
// @Failure      404  {object}  Error
// @Failure      500  {object}  Error
// @Router       /workspaces/{id}/repositories/{repo_id}/files [get]
func (h *GitOpsHandler) GetFileContent(c *gin.Context) {
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	repoID, err := strconv.ParseInt(c.Param("repo_id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid repository ID")
		return
	}
	path := c.Query("path")
	if path == "" {
		BadRequest(c, "path query parameter is required")
		return
	}

	content, err := h.gitOpsSvc.GetFileContent(c.Request.Context(), workspaceID, repoID, path)
	if err != nil {
		slog.Warn("GetFileContent failed", "workspaceID", workspaceID, "repoID", repoID, "path", path, "error", err)
		NotFound(c, "FILE")
		return
	}

	c.JSON(http.StatusOK, FileContentResponse{
		Path:    path,
		Content: content,
		Size:    len(content),
	})
}

type FileContentResponse struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Size    int    `json:"size"`
}

// GetFileDiff returns the diff for a specific file.
// @Summary      Get file diff
// @Description  Get the git diff for a specific file
// @Tags         Git
// @Accept       json
// @Produce      json
// @Param        id        path      string  true  "Workspace ID"
// @Param        repo_id   path      string  true  "Repository ID"
// @Param        path      path      string  true  "File path (supports nested paths)"
// @Success      200  {object}  git.FileDiff
// @Failure      404  {object}  Error
// @Failure      500  {object}  Error
// @Router       /workspaces/{id}/repositories/{repo_id}/diff/{path} [get]
func (h *GitOpsHandler) GetFileDiff(c *gin.Context) {
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	repoID, err := strconv.ParseInt(c.Param("repo_id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid repository ID")
		return
	}
	path := c.Param("path") // Wildcard captures the full path including slashes

	fileDiff, err := h.gitOpsSvc.GetFileDiff(c.Request.Context(), workspaceID, repoID, path)
	if err != nil {
		slog.Warn("GetFileDiff failed", "workspaceID", workspaceID, "repoID", repoID, "path", path, "error", err)
		NotFound(c, "FILE_DIFF")
		return
	}

	c.JSON(http.StatusOK, fileDiff)
}

// GetBranches returns all branches and the current branch.
// @Summary      Get branches
// @Description  Get all branches and the current branch for a repository
// @Tags         Git
// @Accept       json
// @Produce      json
// @Param        id        path      string  true  "Workspace ID"
// @Param        repo_id   path      string  true  "Repository ID"
// @Success      200  {object}  BranchesResponse
// @Failure      404  {object}  Error
// @Failure      500  {object}  Error
// @Router       /workspaces/{id}/repositories/{repo_id}/git/branches [get]
func (h *GitOpsHandler) GetBranches(c *gin.Context) {
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	repoID, err := strconv.ParseInt(c.Param("repo_id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid repository ID")
		return
	}

	if userID := c.GetInt64("auth_user_id"); userID > 0 && h.Authz != nil {
		if _, aerr := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, workspaceID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}

	branches, currentBranch, err := h.gitOpsSvc.Branches(c.Request.Context(), workspaceID, repoID)
	if err != nil {
		slog.Warn("GetBranches failed", "workspaceID", workspaceID, "repoID", repoID, "error", err)
		NotFound(c, "WORKSPACE_REPOSITORY")
		return
	}

	c.JSON(http.StatusOK, BranchesResponse{
		Branches:      branches,
		CurrentBranch: currentBranch,
	})
}
