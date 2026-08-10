package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// IssueHandler exposes the PUT subresource endpoints that replace the entire
// assignees / labels set for an issue. The legacy CreateIssue/UpdateIssue
// strings (kanban handler) are not used here — clients should set arrays via
// these dedicated routes after creating the issue.
type IssueHandler struct {
	svc   *service.KanbanService
	authz *service.Authz
}

func NewIssueHandler(svc *service.KanbanService, authz *service.Authz) *IssueHandler {
	return &IssueHandler{svc: svc, authz: authz}
}

// setAssigneesReq mirrors the CreateIssue body convention: arrays are
// qualified by the resource they belong to (`assignee_user_ids` matches
// `label_ids` in setLabelsReq below). A bare `user_ids` here would be
// inconsistent with how `POST /api/columns/:id/issues` already names the
// field on creation.
type setAssigneesReq struct {
	AssigneeUserIDs []int64 `json:"assignee_user_ids"`
}

type setLabelsReq struct {
	LabelIDs []int64 `json:"label_ids"`
}

// SetAssignees replaces the entire assignee set on the issue. The service
// validates membership against the project's owner; rejected entries surface
// as 400 with code `invalid_assignee`. Caps the slice at 100 — over the limit
// returns 400 `too_many`.
func (h *IssueHandler) SetAssignees(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	issueID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid issue ID")
		return
	}
	if userID > 0 && h.authz != nil {
		if _, err := h.authz.CanAccessIssue(c.Request.Context(), userID, issueID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	var req setAssigneesReq
	if err := c.BindJSON(&req); err != nil {
		BadRequest(c, "bad json")
		return
	}

	d, err := h.svc.SetAssignees(c.Request.Context(), issueID, req.AssigneeUserIDs, userID)
	if err != nil {
		respondIssueWriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": toIssueResponse(d)})
}

// SetLabels replaces the entire label set on the issue. Cross-project label
// ids surface as 400 with code `invalid_label`.
func (h *IssueHandler) SetLabels(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	issueID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid issue ID")
		return
	}
	if userID > 0 && h.authz != nil {
		if _, err := h.authz.CanAccessIssue(c.Request.Context(), userID, issueID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	var req setLabelsReq
	if err := c.BindJSON(&req); err != nil {
		BadRequest(c, "bad json")
		return
	}

	d, err := h.svc.SetLabels(c.Request.Context(), issueID, req.LabelIDs)
	if err != nil {
		respondIssueWriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": toIssueResponse(d)})
}

// respondIssueWriteError maps the sentinel errors returned by the kanban
// service's replaceAssignees/replaceLabels into 400 responses with structured
// codes. Anything unrecognized falls through to the generic 500 handler.
func respondIssueWriteError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidAssignee):
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_assignee", "message": err.Error()})
	case errors.Is(err, service.ErrInvalidLabel):
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_label", "message": err.Error()})
	case errors.Is(err, service.ErrTooMany):
		c.JSON(http.StatusBadRequest, gin.H{"code": "too_many", "message": err.Error()})
	default:
		InternalError(c, err)
	}
}
