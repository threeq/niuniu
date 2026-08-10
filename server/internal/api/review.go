package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

type ReviewHandler struct {
	reviewSvc    *service.ReviewService
	workspaceSvc *service.WorkspaceService
	Authz        *service.Authz
}

func NewReviewHandler(reviewSvc *service.ReviewService, workspaceSvc *service.WorkspaceService) *ReviewHandler {
	return &ReviewHandler{
		reviewSvc:    reviewSvc,
		workspaceSvc: workspaceSvc,
	}
}

// GetDiff returns the aggregated diff for all repositories in a workspace.
// @Summary      Get workspace diff
// @Description  Get aggregated diff for all repositories in a workspace
// @Tags         Review
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Workspace ID"
// @Success      200  {array}   service.RepoDiff
// @Failure      500  {object}  Error
// @Router       /workspaces/{id}/diff [get]
func (h *ReviewHandler) GetDiff(c *gin.Context) {
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}

	if userID := c.GetInt64("auth_user_id"); userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, workspaceID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	// The local-runner sync (#472) fetches with ?patch=1 to get full raw_patch
	// for every file (including resolved repos), which it applies into the bound
	// directory. The SPA list view omits the param and gets the lean summary.
	var diff []service.RepoDiff
	if c.Query("patch") == "1" {
		diff, err = h.reviewSvc.GetDiffWithPatch(c.Request.Context(), workspaceID)
	} else {
		diff, err = h.reviewSvc.GetDiff(c.Request.Context(), workspaceID)
	}
	if err != nil {
		InternalError(c, err)
		return
	}

	c.JSON(http.StatusOK, diff)
}

// GetRepoDiff returns the diff for a specific repository in a workspace.
// @Summary      Get repository diff
// @Description  Get diff for a specific repository in a workspace
// @Tags         Review
// @Accept       json
// @Produce      json
// @Param        id        path      string  true  "Workspace ID"
// @Param        repo_id   path      string  true  "Repository ID"
// @Success      200  {object}  interface{}
// @Failure      404  {object}  Error
// @Failure      500  {object}  Error
// @Router       /workspaces/{id}/repositories/{repo_id}/diff [get]
func (h *ReviewHandler) GetRepoDiff(c *gin.Context) {
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
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, workspaceID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	diff, err := h.reviewSvc.GetRepoDiff(c.Request.Context(), workspaceID, repoID)
	if err != nil {
		slog.Warn("GetRepoDiff failed", "workspaceID", workspaceID, "repoID", repoID, "error", err)
		NotFound(c, "WORKSPACE_REPOSITORY")
		return
	}

	c.JSON(http.StatusOK, diff)
}

// ListComments returns all comments for a workspace.
// @Summary      List comments
// @Description  Get all comments for a workspace
// @Tags         Review
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Workspace ID"
// @Success      200  {array}   CommentResponse
// @Failure      500  {object}  Error
// @Router       /workspaces/{id}/comments [get]
func (h *ReviewHandler) ListComments(c *gin.Context) {
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}

	if userID := c.GetInt64("auth_user_id"); userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, workspaceID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	comments, err := h.reviewSvc.ListComments(c.Request.Context(), workspaceID)
	if err != nil {
		InternalError(c, err)
		return
	}

	c.JSON(http.StatusOK, toCommentResponses(comments))
}

type CreateCommentRequest struct {
	Repo       string `json:"repo"`
	FilePath   string `json:"file_path" binding:"required"`
	LineNumber *int   `json:"line_number"`
	Content    string `json:"content" binding:"required"`
}

// CreateComment creates a new comment for a workspace.
// @Summary      Create a comment
// @Description  Create a new comment for a workspace
// @Tags         Review
// @Accept       json
// @Produce      json
// @Param        id       path      string                  true  "Workspace ID"
// @Param        request  body      CreateCommentRequest  true  "Comment details"
// @Success      201     {object}  CommentResponse
// @Failure      400     {object}  Error
// @Failure      500     {object}  Error
// @Router       /workspaces/{id}/comments [post]
func (h *ReviewHandler) CreateComment(c *gin.Context) {
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}

	if userID := c.GetInt64("auth_user_id"); userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, workspaceID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	if err := h.workspaceSvc.CheckNotArchived(c.Request.Context(), workspaceID); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}

	var req CreateCommentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}

	comment, err := h.reviewSvc.CreateComment(c.Request.Context(), workspaceID, service.CreateCommentInput{
		Repo:       req.Repo,
		FilePath:   req.FilePath,
		LineNumber: req.LineNumber,
		Content:    req.Content,
	})
	if err != nil {
		InternalError(c, err)
		return
	}

	c.JSON(http.StatusCreated, toCommentResponse(comment))
}

// SendCommentToAgent sends a comment to the agent as feedback.
// @Summary      Send comment to agent
// @Description  Send a comment to the agent as feedback
// @Tags         Review
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Comment ID"
// @Success      204
// @Failure      400  {object}  Error
// @Failure      500  {object}  Error
// @Router       /comments/{id}/send-to-agent [post]
func (h *ReviewHandler) SendCommentToAgent(c *gin.Context) {
	commentID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid comment ID")
		return
	}

	if userID := c.GetInt64("auth_user_id"); userID > 0 && h.Authz != nil {
		wsID, werr := h.reviewSvc.CommentWorkspaceID(c.Request.Context(), commentID)
		if werr != nil {
			NotFound(c, "COMMENT")
			return
		}
		if _, aerr := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, wsID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}

	if err := h.reviewSvc.SendCommentToAgent(c.Request.Context(), commentID); err != nil {
		RespondError(c, http.StatusBadRequest, "SEND_FAILED", err.Error())
		return
	}

	c.Status(http.StatusNoContent)
}
