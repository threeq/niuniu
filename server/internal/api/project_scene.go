// Package api: ProjectSceneHandler — REST endpoints for project-level
// "default scenes" prefill list. Workspaces created under a project inherit
// (prefill, not strict-inherit) these scenes — see WorkspaceService.Create.
//
// Routes:
//
//	GET    /api/projects/:id/default-scenes              List
//	POST   /api/projects/:id/default-scenes              Attach
//	DELETE /api/projects/:id/default-scenes/:sceneID     Detach
package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

type ProjectSceneHandler struct {
	q     *store.Queries
	authz *service.Authz
}

func NewProjectSceneHandler(q *store.Queries, authz *service.Authz) *ProjectSceneHandler {
	return &ProjectSceneHandler{q: q, authz: authz}
}

// ProjectDefaultSceneResponse exposes the join row to the SPA. Scene display
// fields (slug/display_name/source) come from the JOIN in ListProjectDefaults.
type ProjectDefaultSceneResponse struct {
	ID          int64  `json:"id"`
	ProjectID   int64  `json:"project_id"`
	SceneID     int64  `json:"scene_id"`
	Position    int64  `json:"position"`
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Source      string `json:"source"`
}

func (h *ProjectSceneHandler) List(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	pid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid project ID")
		return
	}
	if h.authz != nil && userID > 0 {
		if _, err := h.authz.CanAccessProject(c.Request.Context(), userID, pid); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	rows, err := h.q.ListProjectDefaults(c.Request.Context(), pid)
	if err != nil {
		InternalError(c, err)
		return
	}
	out := make([]ProjectDefaultSceneResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, ProjectDefaultSceneResponse{
			ID:          r.ID,
			ProjectID:   r.ProjectID,
			SceneID:     r.SceneID,
			Position:    r.Position,
			Slug:        r.SceneSlug,
			DisplayName: r.SceneDisplayName,
			Source:      r.SceneSource,
		})
	}
	c.JSON(http.StatusOK, out)
}

type attachProjectDefaultRequest struct {
	SceneID  int64 `json:"scene_id" binding:"required"`
	Position *int  `json:"position,omitempty"`
}

func (h *ProjectSceneHandler) Attach(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	pid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid project ID")
		return
	}
	if h.authz != nil && userID > 0 {
		if _, err := h.authz.CanAccessProject(c.Request.Context(), userID, pid); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	var req attachProjectDefaultRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if h.authz != nil && userID > 0 {
		if _, err := h.authz.CanAccessScene(c.Request.Context(), userID, req.SceneID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	var pos int64
	if req.Position != nil {
		pos = int64(*req.Position)
	} else {
		// Default to end of list.
		next, _ := h.q.NextProjectDefaultPosition(c.Request.Context(), pid)
		pos = next
	}
	row, err := h.q.AttachProjectDefault(c.Request.Context(), store.AttachProjectDefaultParams{
		ProjectID: pid,
		SceneID:   req.SceneID,
		Position:  pos,
	})
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"id":         row.ID,
		"project_id": row.ProjectID,
		"scene_id":   row.SceneID,
		"position":   row.Position,
	})
}

func (h *ProjectSceneHandler) Detach(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	pid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid project ID")
		return
	}
	sceneID, err := strconv.ParseInt(c.Param("sceneID"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid scene ID")
		return
	}
	if h.authz != nil && userID > 0 {
		if _, err := h.authz.CanAccessProject(c.Request.Context(), userID, pid); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	if err := h.q.DetachProjectDefault(c.Request.Context(), store.DetachProjectDefaultParams{
		ProjectID: pid,
		SceneID:   sceneID,
	}); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
