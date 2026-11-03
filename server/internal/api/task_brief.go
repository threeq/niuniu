// Task Brief HTTP surface — design §6.9 (GET /api/issues/:id/brief).
package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// TaskBriefHandler serves an issue's current Task Brief + version history.
type TaskBriefHandler struct {
	svc   *service.TaskBriefService
	authz *service.Authz
}

func NewTaskBriefHandler(svc *service.TaskBriefService, authz *service.Authz) *TaskBriefHandler {
	return &TaskBriefHandler{svc: svc, authz: authz}
}

// GetBrief serves GET /api/issues/:id/brief and the MCP tunnel
// GET /mcp/issues/:id/brief. brief is null for non-Intake issues.
//
// @Summary      Read an issue's Task Brief
// @Description  Current brief (highest version) plus all versions, newest first. brief is null for issues that did not come from Intake.
// @Tags         Issues
// @Produce      json
// @Param        id   path      int  true  "Issue ID"
// @Success      200  {object}  service.IssueBriefView
// @Failure      401  {object}  Error
// @Failure      403  {object}  Error
// @Failure      404  {object}  Error
// @Router       /issues/{id}/brief [get]
func (h *TaskBriefHandler) GetBrief(c *gin.Context) {
	issueID, ok := issueIDParam(c)
	if !ok {
		return
	}
	uid := c.GetInt64("auth_user_id")
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	if _, err := h.authz.CanAccessIssue(c.Request.Context(), uid, issueID); err != nil {
		writeAuthzError(c, err)
		return
	}
	view, err := h.svc.GetIssueBrief(c.Request.Context(), issueID)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			NotFound(c, "ISSUE")
			return
		}
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// PutBrief serves PUT /api/issues/:id/brief. The edit appends a new version
// (history is never rewritten) and refreshes the acceptance checklist rows and
// issues.goal_condition in the same transaction.
//
// @Summary      Edit an issue's Task Brief
// @Description  Appends a new version and returns the updated current brief plus the full history. delivery/provenance are inherited from the current version when omitted.
// @Tags         Issues
// @Accept       json
// @Produce      json
// @Param        id    path      int                        true  "Issue ID"
// @Param        body  body      service.TaskBriefInput     true  "Brief fields"
// @Success      200   {object}  service.IssueBriefView
// @Failure      400   {object}  Error
// @Failure      401   {object}  Error
// @Failure      403   {object}  Error
// @Failure      404   {object}  Error
// @Router       /issues/{id}/brief [put]
func (h *TaskBriefHandler) PutBrief(c *gin.Context) {
	issueID, ok := issueIDParam(c)
	if !ok {
		return
	}
	uid := c.GetInt64("auth_user_id")
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	owner, err := h.authz.CanAccessIssue(c.Request.Context(), uid, issueID)
	if err != nil {
		writeAuthzError(c, err)
		return
	}
	if err := h.authz.EnsureOwnerWritable(c.Request.Context(), uid, owner); err != nil {
		writeAuthzError(c, err)
		return
	}
	var in service.TaskBriefInput
	if err := c.ShouldBindJSON(&in); err != nil {
		BadRequest(c, "invalid request body")
		return
	}
	view, err := h.svc.PutIssueBrief(c.Request.Context(), issueID, in)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			NotFound(c, "ISSUE")
			return
		}
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, view)
}

func issueIDParam(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		BadRequest(c, "invalid issue ID")
		return 0, false
	}
	return id, true
}
