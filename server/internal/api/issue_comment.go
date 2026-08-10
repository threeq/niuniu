package api

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

type IssueCommentHandler struct {
	svc   *service.IssueCommentService
	Authz *service.Authz
}

func NewIssueCommentHandler(svc *service.IssueCommentService) *IssueCommentHandler {
	return &IssueCommentHandler{svc: svc}
}

type CreateIssueCommentRequest struct {
	Author  string `json:"author"`
	Content string `json:"content" binding:"required"`
}

type UpdateIssueCommentRequest struct {
	Content string `json:"content" binding:"required"`
}

func (h *IssueCommentHandler) List(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	issueID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid issue ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, aerr := h.Authz.CanAccessIssue(c.Request.Context(), userID, issueID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}
	comments, err := h.svc.ListByIssue(c.Request.Context(), issueID)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, comments)
}

func (h *IssueCommentHandler) Create(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	issueID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid issue ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, aerr := h.Authz.CanAccessIssue(c.Request.Context(), userID, issueID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}
	var req CreateIssueCommentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	comment, err := h.svc.Create(c.Request.Context(), issueID, req.Author, userID, req.Content)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusCreated, comment)
}

func (h *IssueCommentHandler) Update(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("commentId"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid comment ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		comment, cerr := h.svc.Get(c.Request.Context(), id)
		if cerr != nil {
			NotFound(c, "COMMENT")
			return
		}
		if _, aerr := h.Authz.CanAccessIssue(c.Request.Context(), userID, comment.IssueID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}
	var req UpdateIssueCommentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	comment, err := h.svc.Update(c.Request.Context(), id, req.Content)
	if err != nil {
		slog.Warn("UpdateIssueComment failed", "commentID", id, "error", err)
		NotFound(c, "COMMENT")
		return
	}
	c.JSON(http.StatusOK, comment)
}

func (h *IssueCommentHandler) Delete(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("commentId"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid comment ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		comment, cerr := h.svc.Get(c.Request.Context(), id)
		if cerr != nil {
			NotFound(c, "COMMENT")
			return
		}
		if _, aerr := h.Authz.CanAccessIssue(c.Request.Context(), userID, comment.IssueID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
