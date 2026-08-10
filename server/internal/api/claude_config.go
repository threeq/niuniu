package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// ClaudeConfigHandler exposes account-scoped endpoints for reading and
// toggling the global plugin/MCP configuration in a claude account's
// configDir (i.e. the ~/.claude/settings.json equivalent), plus library
// operations (install/uninstall plugins, add marketplaces).
//
//	GET  /api/claude-accounts/:id/config                              — read plugins + mcp toggles
//	PUT  /api/claude-accounts/:id/plugins/:pluginId                   — enable/disable a plugin
//	PUT  /api/claude-accounts/:id/mcp/:name                           — enable/disable an MCP server
//	POST /api/claude-accounts/:id/library/plugins/:pluginId/install   — install a plugin
//	POST /api/claude-accounts/:id/library/plugins/:pluginId/uninstall — uninstall a plugin
//	POST /api/claude-accounts/:id/library/marketplaces                — add a marketplace
type ClaudeConfigHandler struct {
	cfgSvc       *service.ClaudeConfigService
	sceneSvc     *service.SceneService
	accountAuthz accountAuthorizer
}

func NewClaudeConfigHandler(cfg *service.ClaudeConfigService, _ *service.ClaudeAccountService, authz accountAuthorizer, sceneSvc *service.SceneService) *ClaudeConfigHandler {
	return &ClaudeConfigHandler{cfgSvc: cfg, sceneSvc: sceneSvc, accountAuthz: authz}
}

func (h *ClaudeConfigHandler) caller(c *gin.Context) (int64, string) {
	idVal, _ := c.Get("auth_user_id")
	roleVal, _ := c.Get("auth_role")
	uid, _ := idVal.(int64)
	role, _ := roleVal.(string)
	return uid, role
}

func (h *ClaudeConfigHandler) parseAccountID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad account id"})
		return 0, false
	}
	return id, true
}

// resolveConfigDir checks account access then returns its configDir.
// Uses the ResolvedAccount returned by CanAccessClaudeAccount directly,
// avoiding the redundant GetByID call.
func (h *ClaudeConfigHandler) resolveConfigDir(c *gin.Context, accountID int64) (string, bool) {
	uid, role := h.caller(c)
	acc, err := h.accountAuthz.CanAccessClaudeAccount(c.Request.Context(), service.NewCallerInfo(uid, role), accountID)
	if err != nil || acc == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
		return "", false
	}
	return acc.ConfigDir, true
}

// Get — GET /api/claude-accounts/:id/config
func (h *ClaudeConfigHandler) Get(c *gin.Context) {
	id, ok := h.parseAccountID(c)
	if !ok {
		return
	}
	dir, ok := h.resolveConfigDir(c, id)
	if !ok {
		return
	}
	view, err := h.cfgSvc.List(dir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Apply Featured flag from builtin scenes (non-fatal if unavailable).
	if h.sceneSvc != nil {
		if featured, err := h.sceneSvc.FeaturedPluginSources(c.Request.Context()); err == nil {
			for i := range view.Plugins {
				if featured[view.Plugins[i].ID] {
					view.Plugins[i].Featured = true
				}
			}
		}
	}
	c.JSON(http.StatusOK, view)
}

type claudeToggleRequest struct {
	Enabled bool `json:"enabled"`
}

// PutPlugin — PUT /api/claude-accounts/:id/plugins/:pluginId
func (h *ClaudeConfigHandler) PutPlugin(c *gin.Context) {
	id, ok := h.parseAccountID(c)
	if !ok {
		return
	}
	dir, ok := h.resolveConfigDir(c, id)
	if !ok {
		return
	}
	var req claudeToggleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.cfgSvc.SetPlugin(dir, c.Param("pluginId"), req.Enabled); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"enabled": req.Enabled, "restart_required": true})
}

// PutMCP — PUT /api/claude-accounts/:id/mcp/:name
func (h *ClaudeConfigHandler) PutMCP(c *gin.Context) {
	id, ok := h.parseAccountID(c)
	if !ok {
		return
	}
	dir, ok := h.resolveConfigDir(c, id)
	if !ok {
		return
	}
	var req claudeToggleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.cfgSvc.SetMCP(dir, c.Param("name"), req.Enabled); err != nil {
		if errors.Is(err, service.ErrMCPServerNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "mcp server not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"enabled": req.Enabled, "restart_required": true})
}

type claudeInstallPluginRequest struct {
	Ref string `json:"ref"`
}

// InstallPlugin — POST /api/claude-accounts/:id/library/plugins/:pluginId/install
func (h *ClaudeConfigHandler) InstallPlugin(c *gin.Context) {
	id, ok := h.parseAccountID(c)
	if !ok {
		return
	}
	dir, ok := h.resolveConfigDir(c, id)
	if !ok {
		return
	}
	var req claudeInstallPluginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.cfgSvc.InstallPlugin(c.Request.Context(), dir, c.Param("pluginId"), req.Ref); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "restart_required": true})
}

// UninstallPlugin — POST /api/claude-accounts/:id/library/plugins/:pluginId/uninstall
func (h *ClaudeConfigHandler) UninstallPlugin(c *gin.Context) {
	id, ok := h.parseAccountID(c)
	if !ok {
		return
	}
	dir, ok := h.resolveConfigDir(c, id)
	if !ok {
		return
	}
	if err := h.cfgSvc.UninstallPlugin(c.Request.Context(), dir, c.Param("pluginId")); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "restart_required": true})
}

type claudeAddMarketplaceRequest struct {
	Source string `json:"source"`
}

// AddMarketplace — POST /api/claude-accounts/:id/library/marketplaces
func (h *ClaudeConfigHandler) AddMarketplace(c *gin.Context) {
	id, ok := h.parseAccountID(c)
	if !ok {
		return
	}
	dir, ok := h.resolveConfigDir(c, id)
	if !ok {
		return
	}
	var req claudeAddMarketplaceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Source == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "source is required"})
		return
	}
	if err := h.cfgSvc.AddMarketplace(c.Request.Context(), dir, req.Source); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "restart_required": true})
}
