// Package api: ad-hoc "install one plugin" endpoint used by the chat-input
// skill-install dialog. The scene-driven install path (workspace_scene.go)
// manages plugins declared by scene layers; this endpoint lets a user pick
// a curated skill from the dialog and install it directly without attaching
// a scene layer first.
package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// PluginInstallHandler handles plugin-install and marketplace-add endpoints
// for the chat-input skill dialog. Both paths share the same scope routing
// (global vs workspace-bound-account ConfigDir) and authz gate, so they live
// on a single handler struct.
type PluginInstallHandler struct {
	pluginInst    *service.PluginInstaller
	marketplace   *service.MarketplaceManager
	authz         *service.Authz
	q             *store.Queries
	authEnabled   bool
}

// NewPluginInstallHandler wires the dependencies. marketplace / claudeAccount
// / authz / q may be nil in stripped-down tests; in that case "workspace"
// scope falls back to global (configDir="") and no authz gate is applied.
func NewPluginInstallHandler(
	pluginInst *service.PluginInstaller,
	marketplace *service.MarketplaceManager,
	authz *service.Authz,
	q *store.Queries,
	authEnabled bool,
) *PluginInstallHandler {
	return &PluginInstallHandler{
		pluginInst:    pluginInst,
		marketplace:   marketplace,
		authz:         authz,
		q:             q,
		authEnabled:   authEnabled,
	}
}

// pluginInstallRequest body:
//
//	scope        "global" → ~/.claude/; "workspace" → the workspace's bound
//	             claude-account ConfigDir (falls back to ~/.claude/ if no
//	             account is bound).
//	source       canonical plugin source ("name@marketplace" or git URL).
//	ref          optional version pin.
//	workspace_id required when scope == "workspace".
type pluginInstallRequest struct {
	Scope       string `json:"scope" binding:"required"`
	Source      string `json:"source" binding:"required"`
	Ref         string `json:"ref"`
	WorkspaceID int64  `json:"workspace_id"`
}

type pluginInstallResponse struct {
	Source string `json:"source"`
	Ref    string `json:"ref,omitempty"`
	Status string `json:"status"`
	Stderr string `json:"stderr,omitempty"`
}

// Install runs `claude plugin install <source> [--ref <ref>]` against the
// resolved CLAUDE_CONFIG_DIR. Synchronous — install is fast enough (<30s on
// a cold network cache) that a job table would be overkill, and the user is
// staring at the dialog waiting for the result.
func (h *PluginInstallHandler) Install(c *gin.Context) {
	var body pluginInstallRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, "invalid body: "+err.Error())
		return
	}
	if body.Scope != "global" && body.Scope != "workspace" {
		BadRequest(c, "scope must be 'global' or 'workspace'")
		return
	}

	configDir, cliType, ok := h.resolveScope(c, body.Scope, body.WorkspaceID)
	if !ok {
		return
	}

	results := h.pluginInst.ApplyForCLI(c.Request.Context(), cliType, configDir, []service.PluginDecl{
		{Source: body.Source, Ref: body.Ref},
	})
	if len(results) == 0 {
		InternalError(c, errors.New("plugin installer returned no result"))
		return
	}
	r := results[0]
	c.JSON(http.StatusOK, pluginInstallResponse{
		Source: r.Source,
		Ref:    r.Ref,
		Status: string(r.Status),
		Stderr: r.Stderr,
	})
}

// addMarketplaceRequest body mirrors pluginInstallRequest — same scope /
// workspace_id semantics, but no ref (marketplaces don't have a version).
type addMarketplaceRequest struct {
	Scope       string `json:"scope" binding:"required"`
	Source      string `json:"source" binding:"required"`
	WorkspaceID int64  `json:"workspace_id"`
}

type addMarketplaceResponse struct {
	Source string `json:"source"`
	OK     bool   `json:"ok"`
	Stderr string `json:"stderr,omitempty"`
}

// AddMarketplace runs `claude plugin marketplace add <source>` against the
// resolved CLAUDE_CONFIG_DIR. Returns 200 with ok=false + stderr when the CLI
// fails (re-add of an existing marketplace, network failure, etc.) so the
// SPA can render the message inline instead of triggering a hard error.
func (h *PluginInstallHandler) AddMarketplace(c *gin.Context) {
	var body addMarketplaceRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, "invalid body: "+err.Error())
		return
	}
	if body.Scope != "global" && body.Scope != "workspace" {
		BadRequest(c, "scope must be 'global' or 'workspace'")
		return
	}

	configDir, cliType, ok := h.resolveScope(c, body.Scope, body.WorkspaceID)
	if !ok {
		return
	}
	if h.marketplace == nil {
		InternalError(c, errors.New("marketplace manager not configured"))
		return
	}

	res := h.marketplace.AddForCLI(c.Request.Context(), cliType, configDir, body.Source)
	c.JSON(http.StatusOK, addMarketplaceResponse{
		Source: res.Source,
		OK:     res.OK,
		Stderr: res.Stderr,
	})
}

// pluginUninstallRequest body — same scope semantics as install.
type pluginUninstallRequest struct {
	Scope       string `json:"scope" binding:"required"`
	Source      string `json:"source" binding:"required"`
	WorkspaceID int64  `json:"workspace_id"`
}

// Uninstall removes a previously-installed plugin from the resolved
// CLAUDE_CONFIG_DIR.
func (h *PluginInstallHandler) Uninstall(c *gin.Context) {
	var body pluginUninstallRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, "invalid body: "+err.Error())
		return
	}
	if body.Scope != "global" && body.Scope != "workspace" {
		BadRequest(c, "scope must be 'global' or 'workspace'")
		return
	}

	configDir, cliType, ok := h.resolveScope(c, body.Scope, body.WorkspaceID)
	if !ok {
		return
	}

	r := h.pluginInst.UninstallForCLI(c.Request.Context(), cliType, configDir, service.PluginDecl{Source: body.Source})
	c.JSON(http.StatusOK, pluginInstallResponse{
		Source: r.Source,
		Ref:    r.Ref,
		Status: string(r.Status),
		Stderr: r.Stderr,
	})
}

// checkInstalledRequest body — same scope semantics; sources is the
// (non-empty) list to check.
type checkInstalledRequest struct {
	Scope       string   `json:"scope" binding:"required"`
	Sources     []string `json:"sources" binding:"required"`
	WorkspaceID int64    `json:"workspace_id"`
}

// CheckInstalled returns the subset of `sources` already installed under the
// resolved CLAUDE_CONFIG_DIR. The SPA uses this to badge installed rows and
// offer uninstall instead of install on the curated list.
func (h *PluginInstallHandler) CheckInstalled(c *gin.Context) {
	var body checkInstalledRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, "invalid body: "+err.Error())
		return
	}
	if body.Scope != "global" && body.Scope != "workspace" {
		BadRequest(c, "scope must be 'global' or 'workspace'")
		return
	}
	if len(body.Sources) == 0 {
		c.JSON(http.StatusOK, gin.H{"installed": []string{}})
		return
	}

	configDir, cliType, ok := h.resolveScope(c, body.Scope, body.WorkspaceID)
	if !ok {
		return
	}

	installed := h.pluginInst.BatchIsInstalledForCLI(c.Request.Context(), cliType, configDir, body.Sources)
	c.JSON(http.StatusOK, gin.H{"installed": installed, "cli_type": cliType})
}

// resolveScope centralizes scope -> (configDir, cliType) resolution plus
// authz. Returns ok=false (and writes the error response) on validation
// failure, missing workspace_id, or denied access.
//
//   - scope="global"     → configDir="" + cliType="claude" (the default
//     niuniu install target; Codex global installs are not exposed in
//     the dialog right now, but the codex CLI is reachable when needed
//     via the workspace scope.)
//   - scope="workspace"  → workspace's bound-account ConfigDir + workspace's
//     cli_type (claude | codex).
func (h *PluginInstallHandler) resolveScope(c *gin.Context, scope string, wsID int64) (configDir, cliType string, ok bool) {
	if scope == "global" {
		if !h.canMutateGlobal(c) {
			c.JSON(http.StatusForbidden, gin.H{
				"error": gin.H{"code": "FORBIDDEN", "message": "admin required for global plugin changes"},
			})
			return "", "", false
		}
		return "", "claude", true
	}
	if wsID == 0 {
		BadRequest(c, "workspace_id required when scope='workspace'")
		return "", "", false
	}
	userID := c.GetInt64("auth_user_id")
	if h.authz != nil && userID > 0 {
		if _, err := h.authz.CanAccessWorkspace(c.Request.Context(), userID, wsID); err != nil {
			writeAuthzError(c, err)
			return "", "", false
		}
	}
	if h.q == nil {
		return "", "claude", true
	}
	ws, err := h.q.GetWorkspace(c.Request.Context(), wsID)
	if err != nil {
		InternalError(c, err)
		return "", "", false
	}
	cli := ws.CliType
	if cli != "codex" {
		cli = "claude"
	}
	return "", cli, true
}

func (h *PluginInstallHandler) canMutateGlobal(c *gin.Context) bool {
	if !h.authEnabled {
		return true
	}
	role, _ := c.Get("auth_role")
	roleStr, _ := role.(string)
	return roleStr == "admin" || roleStr == "owner"
}
