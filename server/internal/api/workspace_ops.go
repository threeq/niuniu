package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

const maxCommitMessageLength = 10000

type WorkspaceOpsHandler struct {
	Svc          *service.WorkspaceOpsService
	workspaceSvc *service.WorkspaceService
	Authz        *service.Authz
}

func NewWorkspaceOpsHandler(svc *service.WorkspaceOpsService, workspaceSvc *service.WorkspaceService) *WorkspaceOpsHandler {
	return &WorkspaceOpsHandler{Svc: svc, workspaceSvc: workspaceSvc}
}

func (h *WorkspaceOpsHandler) Commit(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	if err := h.workspaceSvc.CheckNotArchived(c.Request.Context(), id); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}

	name := c.Param("name")
	if len(name) > 0 && name[0] == '/' {
		name = name[1:]
	}
	var req struct {
		Message string `json:"message" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "message is required")
		return
	}
	if len(req.Message) > maxCommitMessageLength {
		BadRequest(c, "commit message too long (max 10000 chars)")
		return
	}
	if err := h.Svc.CommitWorktree(c.Request.Context(), id, name, req.Message); err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// WriteWorktreeFile handles PUT /workspaces/:id/worktrees/:name/file.
//
// Writes a text file into the worktree so it lands in the git working tree and
// shows up (diffable) in the Changes panel. Used by the embedded-canvas bridge
// to persist editor source files (e.g. `.excalidraw`) alongside the exported
// image. Body: { "path": "<worktree-relative path>", "content": "<text>" }.
func (h *WorkspaceOpsHandler) WriteWorktreeFile(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	if err := h.workspaceSvc.CheckNotArchived(c.Request.Context(), id); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}

	name := c.Param("name")
	if len(name) > 0 && name[0] == '/' {
		name = name[1:]
	}
	var req struct {
		Path    string `json:"path" binding:"required"`
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "path is required")
		return
	}

	written, err := h.workspaceSvc.WriteWorktreeFile(c.Request.Context(), id, name, req.Path, req.Content)
	if err != nil {
		// Bad-input errors (invalid path/name, oversized, missing worktree)
		// surface as 400; the client sends these programmatically.
		msg := err.Error()
		if strings.Contains(msg, "invalid") || strings.Contains(msg, "not allowed") ||
			strings.Contains(msg, "too large") || strings.Contains(msg, "worktree not found") {
			BadRequest(c, msg)
			return
		}
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"path": written})
}

func (h *WorkspaceOpsHandler) Merge(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	if err := h.workspaceSvc.CheckNotArchived(c.Request.Context(), id); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}

	name := c.Param("name")
	if len(name) > 0 && name[0] == '/' {
		name = name[1:]
	}
	var req struct {
		TargetBranch string `json:"target_branch" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "target_branch is required")
		return
	}
	if strings.HasPrefix(req.TargetBranch, "-") {
		BadRequest(c, "target_branch must not start with '-'")
		return
	}
	if err := h.Svc.MergeWorktree(c.Request.Context(), id, name, req.TargetBranch); err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *WorkspaceOpsHandler) Push(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	if err := h.workspaceSvc.CheckNotArchived(c.Request.Context(), id); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}

	name := c.Param("name")
	if len(name) > 0 && name[0] == '/' {
		name = name[1:]
	}
	if err := h.Svc.PushWorktree(c.Request.Context(), id, name); err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *WorkspaceOpsHandler) GenerateCommitMessage(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	name := c.Param("name")
	if len(name) > 0 && name[0] == '/' {
		name = name[1:]
	}
	message, err := h.Svc.GenerateCommitMessage(c.Request.Context(), id, name)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": message})
}

func (h *WorkspaceOpsHandler) Complete(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	if err := h.workspaceSvc.CheckNotArchived(c.Request.Context(), id); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}

	var req struct {
		Mode      string                           `json:"mode" binding:"required"`
		Worktrees []service.WorktreeCompletionStep `json:"worktrees" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if req.Mode != "merge" && req.Mode != "push" {
		BadRequest(c, "mode must be 'merge' or 'push'")
		return
	}
	for _, wt := range req.Worktrees {
		if len(wt.CommitMessage) > maxCommitMessageLength {
			BadRequest(c, "commit message too long (max 10000 chars) for worktree "+wt.Name)
			return
		}
	}
	results, err := h.Svc.CompleteWorkspace(c.Request.Context(), id, req.Mode, req.Worktrees)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"results": results,
			"error":   err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"results": results})
}

// MarkDone flips workspace.status to 'completed' (and propagates issue
// lifecycle in the service layer) without running any git operations.
// Refuses with 409 if the workspace is currently running, and 403 if archived.
func (h *WorkspaceOpsHandler) MarkDone(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	if err := h.workspaceSvc.CheckNotArchived(c.Request.Context(), id); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}

	// Human mark-done routes through the single completion choke point: it runs the
	// project's column-native floor gate first (§22). When the project has no floor
	// gate the workspace finalizes inline and we return the legacy {"status":"ok"}
	// (+ partial-success warnings). When a floor gate runs it is async — we return
	// {"status":"gate_checking"} and the terminal outcome lands on issue.exec_status
	// ('done' | 'gate_blocked') + a gate_done event the SPA renders (§22.3 human path).
	res, err := h.Svc.RequestWorkspaceCompletion(c.Request.Context(), id, service.TriggerHuman)
	if err != nil {
		if errors.Is(err, service.ErrWorkspaceRunning) {
			RespondErrorWithDetails(c, http.StatusConflict, "WORKSPACE_RUNNING", "运行中的工作空间不能直接标记完成，请先停止 agent", nil)
			return
		}
		InternalError(c, err)
		return
	}
	resp := gin.H{"status": "ok"}
	if res.Status == service.CompletionGateChecking {
		resp["status"] = "gate_checking"
	}
	if len(res.Warnings) > 0 {
		// Partial success: workspace.status flipped but a downstream side-effect
		// failed (e.g. issue lifecycle sync). Surface warnings so the SPA renders
		// a non-blocking toast.warning instead of toast.success.
		resp["warnings"] = res.Warnings
	}
	c.JSON(http.StatusOK, resp)
}

// UnmarkDone reverts a just-marked-done workspace back to a previous status
// supplied by the caller. Intended for the SPA's "Undo" toast that fires
// within seconds of MarkDone — see useMarkWorkspaceDone on the frontend.
//
// Returns:
//   - 200 + {"status":"ok"} on success
//   - 400 if previous_status is missing or outside the allow-list
//     ('created' | 'needs_review' | 'attention' | 'paused')
//   - 409 if the workspace is no longer 'completed' (undo is stale)
//   - 403 if the workspace is archived
func (h *WorkspaceOpsHandler) UnmarkDone(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	if err := h.workspaceSvc.CheckNotArchived(c.Request.Context(), id); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}

	var body struct {
		PreviousStatus string `json:"previous_status"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, "invalid body")
		return
	}
	body.PreviousStatus = strings.TrimSpace(body.PreviousStatus)
	if body.PreviousStatus == "" {
		BadRequest(c, "previous_status is required")
		return
	}

	if err := h.Svc.UnmarkWorkspaceDone(c.Request.Context(), id, body.PreviousStatus); err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidPreviousStatus):
			RespondErrorWithDetails(c, http.StatusBadRequest, "INVALID_PREVIOUS_STATUS", "previous_status 不在允许列表中", nil)
		case errors.Is(err, service.ErrWorkspaceNotCompleted):
			RespondErrorWithDetails(c, http.StatusConflict, "WORKSPACE_NOT_COMPLETED", "工作空间已不在已完成状态，无法撤销", nil)
		default:
			InternalError(c, err)
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
