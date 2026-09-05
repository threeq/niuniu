package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// ProjectFloorHandler serves the per-project 底线 command REST surface: the single
// shell command that must exit 0 before an issue may complete.
type ProjectFloorHandler struct {
	svc   *service.ProjectFloorService
	authz *service.Authz
}

func NewProjectFloorHandler(svc *service.ProjectFloorService, authz *service.Authz) *ProjectFloorHandler {
	return &ProjectFloorHandler{svc: svc, authz: authz}
}

// accessProject parses :id and enforces project access, mirroring the cleanup
// handler's helper.
func (h *ProjectFloorHandler) accessProject(c *gin.Context) (int64, bool) {
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

// GetFloor returns a project's 底线 command.
func (h *ProjectFloorHandler) GetFloor(c *gin.Context) {
	pid, ok := h.accessProject(c)
	if !ok {
		return
	}
	f, err := h.svc.GetFloor(c.Request.Context(), pid)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, f)
}

// SetFloor validates and stores a project's 底线 command. A blank command clears it.
func (h *ProjectFloorHandler) SetFloor(c *gin.Context) {
	pid, ok := h.accessProject(c)
	if !ok {
		return
	}
	var req service.ProjectFloor
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "invalid request body")
		return
	}
	if err := h.svc.SetFloor(c.Request.Context(), pid, req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	// Echo back the normalized, persisted floor.
	f, err := h.svc.GetFloor(c.Request.Context(), pid)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, f)
}
