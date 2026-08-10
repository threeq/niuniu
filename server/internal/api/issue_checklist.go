package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

type IssueChecklistHandler struct {
	svc   *service.IssueChecklistService
	Authz *service.Authz
}

func NewIssueChecklistHandler(svc *service.IssueChecklistService) *IssueChecklistHandler {
	return &IssueChecklistHandler{svc: svc}
}

type CreateChecklistRequest struct {
	Title string `json:"title" binding:"required"`
}

type UpdateChecklistRequest struct {
	Title       string `json:"title" binding:"required"`
	IsCompleted int64  `json:"is_completed"`
}

type UpdateChecklistPositionRequest struct {
	Position int64 `json:"position"`
}

func (h *IssueChecklistHandler) List(c *gin.Context) {
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
	items, err := h.svc.ListByIssue(c.Request.Context(), issueID)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, items)
}

func (h *IssueChecklistHandler) Create(c *gin.Context) {
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
	var req CreateChecklistRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	item, err := h.svc.Create(c.Request.Context(), issueID, req.Title)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusCreated, item)
}

func (h *IssueChecklistHandler) Update(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("checklistId"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid checklist ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		item, cerr := h.svc.Get(c.Request.Context(), id)
		if cerr != nil {
			NotFound(c, "CHECKLIST")
			return
		}
		if _, aerr := h.Authz.CanAccessIssue(c.Request.Context(), userID, item.IssueID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}
	var req UpdateChecklistRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	item, err := h.svc.Update(c.Request.Context(), id, req.Title, req.IsCompleted)
	if err != nil {
		NotFound(c, "CHECKLIST")
		return
	}
	c.JSON(http.StatusOK, item)
}

func (h *IssueChecklistHandler) UpdatePosition(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("checklistId"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid checklist ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		item, cerr := h.svc.Get(c.Request.Context(), id)
		if cerr != nil {
			NotFound(c, "CHECKLIST")
			return
		}
		if _, aerr := h.Authz.CanAccessIssue(c.Request.Context(), userID, item.IssueID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}
	var req UpdateChecklistPositionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if err := h.svc.UpdatePosition(c.Request.Context(), id, req.Position); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *IssueChecklistHandler) Delete(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("checklistId"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid checklist ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		item, cerr := h.svc.Get(c.Request.Context(), id)
		if cerr != nil {
			NotFound(c, "CHECKLIST")
			return
		}
		if _, aerr := h.Authz.CanAccessIssue(c.Request.Context(), userID, item.IssueID); aerr != nil {
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
