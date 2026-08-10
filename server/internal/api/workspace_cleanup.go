package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// WorkspaceCleanupHandler serves the per-project workspace auto-cleanup policy
// REST surface (get / set / run-once).
type WorkspaceCleanupHandler struct {
	svc   *service.WorkspaceCleanupService
	authz *service.Authz
}

func NewWorkspaceCleanupHandler(svc *service.WorkspaceCleanupService, authz *service.Authz) *WorkspaceCleanupHandler {
	return &WorkspaceCleanupHandler{svc: svc, authz: authz}
}

// accessProject parses :id and enforces project access, mirroring the memory
// handler's helper.
func (h *WorkspaceCleanupHandler) accessProject(c *gin.Context) (int64, bool) {
	pid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || pid <= 0 {
		BadRequest(c, "invalid project ID")
		return 0, false
	}
	userID := c.GetInt64("auth_user_id")
	if h.authz != nil && userID > 0 {
		if _, err := h.authz.CanAccessProject(c.Request.Context(), userID, pid); err != nil {
			writeAuthzError(c, err)
			return 0, false
		}
	}
	return pid, true
}

// GetCleanupPolicy returns a project's workspace auto-cleanup policy.
func (h *WorkspaceCleanupHandler) GetCleanupPolicy(c *gin.Context) {
	pid, ok := h.accessProject(c)
	if !ok {
		return
	}
	pol, err := h.svc.GetPolicy(c.Request.Context(), pid)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, pol)
}

// SetCleanupPolicy validates and stores a project's workspace auto-cleanup policy.
func (h *WorkspaceCleanupHandler) SetCleanupPolicy(c *gin.Context) {
	pid, ok := h.accessProject(c)
	if !ok {
		return
	}
	var req service.CleanupPolicy
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "invalid request body")
		return
	}
	if req.Enabled && req.InactiveDays <= 0 {
		BadRequest(c, "inactive_days must be a positive number of days when cleanup is enabled")
		return
	}
	if err := h.svc.SetPolicy(c.Request.Context(), pid, req); err != nil {
		InternalError(c, err)
		return
	}
	// Echo back the normalized, persisted policy.
	pol, err := h.svc.GetPolicy(c.Request.Context(), pid)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, pol)
}

// RunCleanupOnce triggers a single cleanup sweep for a project regardless of the
// hourly schedule (the manual "clean now" action). Returns the sweep summary.
func (h *WorkspaceCleanupHandler) RunCleanupOnce(c *gin.Context) {
	pid, ok := h.accessProject(c)
	if !ok {
		return
	}
	res, err := h.svc.SweepProject(c.Request.Context(), pid)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}
