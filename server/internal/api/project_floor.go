package api

import (
	"net/http"
	"strconv"
	"strings"

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

// ListFloors returns the floors of the projects named by ?ids=1,2,3. Ids the
// caller cannot access are dropped rather than failing the whole request, so the
// engineering-standards overview degrades to the visible subset instead of erroring.
func (h *ProjectFloorHandler) ListFloors(c *gin.Context) {
	raw := strings.TrimSpace(c.Query("ids"))
	if raw == "" {
		c.JSON(http.StatusOK, []service.ProjectFloorSummary{})
		return
	}
	userID := c.GetInt64("auth_user_id")
	ids := make([]int64, 0, 8)
	for _, part := range strings.Split(raw, ",") {
		pid, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil || pid <= 0 {
			BadRequest(c, "invalid project ID in ids")
			return
		}
		if h.authz != nil && userID > 0 {
			if _, err := h.authz.CanAccessProject(c.Request.Context(), userID, pid); err != nil {
				continue
			}
		}
		ids = append(ids, pid)
	}
	floors, err := h.svc.ListFloors(c.Request.Context(), ids)
	if err != nil {
		InternalError(c, err)
		return
	}
	if floors == nil {
		floors = []service.ProjectFloorSummary{}
	}
	c.JSON(http.StatusOK, floors)
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
