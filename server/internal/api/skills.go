// Package api: skill management endpoints (SkillsGate-style console).
//
// Routes (wired in server/internal/server/router.go):
//
//	GET  /api/skills?workspace_id=   Catalog + install/enable state per agent/scope
//	POST /api/skills/install         Global install (store copy / plugin install)
//	POST /api/skills/enable          Enable at targets (agent x scope)
//	POST /api/skills/disable         Disable at targets (store/installs kept)
//	POST /api/skills/update          Refresh store + all enabled copies
//	POST /api/skills/uninstall       Remove globally (store + enabled copies)
//
// Install != enable (issue #666): a globally installed skill is disabled by
// default and only becomes agent-visible when enabled manually or by a scene
// (workspace-scoped). Global-scope operations act on the signed-in user's own
// machine (the same trust boundary as /api/plugins/install); workspace-scope
// operations pass the workspace authz gate first.
package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// SkillsHandler exposes the cross-agent SkillManager over REST.
type SkillsHandler struct {
	skills *service.SkillManager
	authz  *service.Authz
}

// NewSkillsHandler wires the handler. authz may be nil (stripped-down tests);
// workspace-scope calls then skip the access gate.
func NewSkillsHandler(skills *service.SkillManager, authz *service.Authz) *SkillsHandler {
	return &SkillsHandler{skills: skills, authz: authz}
}

// canAccessWorkspace gates workspace-scoped operations. Returns true when no
// authz is wired or the user may access the workspace.
func (h *SkillsHandler) canAccessWorkspace(c *gin.Context, workspaceID int64) bool {
	if h.authz == nil || workspaceID <= 0 {
		return true
	}
	userID := c.GetInt64("auth_user_id")
	if userID == 0 {
		return true // auth disabled
	}
	_, err := h.authz.CanAccessWorkspace(c.Request.Context(), userID, workspaceID)
	return err == nil
}

// List returns the skill catalog enriched with install/enable state. An
// optional workspace_id adds that workspace's scope to the scan.
func (h *SkillsHandler) List(c *gin.Context) {
	var workspaceID int64
	if raw := c.Query("workspace_id"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			BadRequest(c, "invalid workspace_id")
			return
		}
		workspaceID = v
	}
	if workspaceID > 0 && !h.canAccessWorkspace(c, workspaceID) {
		writeAuthzError(c, service.ErrForbidden)
		return
	}
	c.JSON(http.StatusOK, h.skills.List(c.Request.Context(), workspaceID))
}

// skillNameRequest is the body for the name-only actions (install / update /
// uninstall - all global-scope).
type skillNameRequest struct {
	Name string `json:"name" binding:"required"`
}

// Install performs a global install (store copy for builtin skills; plugin
// install, default-disabled, for marketplace skills).
func (h *SkillsHandler) Install(c *gin.Context) {
	var req skillNameRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if err := h.skills.Install(c.Request.Context(), req.Name); err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// Enable makes a skill agent-visible at one or more targets (agent x scope).
func (h *SkillsHandler) Enable(c *gin.Context) {
	var req service.SkillTargetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if !h.canAccessWorkspace(c, req.WorkspaceID) {
		writeAuthzError(c, service.ErrForbidden)
		return
	}
	c.JSON(http.StatusOK, gin.H{"results": h.skills.Enable(c.Request.Context(), req)})
}

// Disable removes a skill's agent visibility at one or more targets (the
// global install is kept). User-authored directories are refused server-side.
func (h *SkillsHandler) Disable(c *gin.Context) {
	var req service.SkillTargetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if !h.canAccessWorkspace(c, req.WorkspaceID) {
		writeAuthzError(c, service.ErrForbidden)
		return
	}
	c.JSON(http.StatusOK, gin.H{"results": h.skills.Disable(c.Request.Context(), req)})
}

// Update refreshes the store and every niuniu-managed enabled copy of one
// skill with the latest embedded payload.
func (h *SkillsHandler) Update(c *gin.Context) {
	var req skillNameRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if err := h.skills.Update(c.Request.Context(), req.Name); err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// Uninstall removes a skill globally (store + all enabled copies; marketplace
// plugins delegate to `claude plugin uninstall`).
func (h *SkillsHandler) Uninstall(c *gin.Context) {
	var req skillNameRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if err := h.skills.Uninstall(c.Request.Context(), req.Name); err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
