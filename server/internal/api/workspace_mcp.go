package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/niuniu-dev/niuniu/internal/service"
)

// WorkspaceMCPHandler hosts the 5 endpoints that manage per-workspace MCP
// configuration. Routes are registered under /api in router.go.
//
//   GET  /api/claude-accounts/:id/mcp/available    — list MCPs visible to the account
//   POST /api/workspaces/mcp/detect                — recommend MCPs for a (account, repos) pair (pre-create)
//   GET  /api/workspaces/:id/mcp                   — read the workspace's current selection + availability
//   PUT  /api/workspaces/:id/mcp                   — replace selection, rewrite .mcp.json
//   POST /api/workspaces/:id/mcp/redetect          — re-run the detector for an existing workspace
type WorkspaceMCPHandler struct {
	wsSvc        *service.WorkspaceService
	acctSvc      *service.ClaudeAccountService
	registry     *service.ClaudeMCPRegistry
	detector     *service.WorkspaceMCPDetector
	accountAuthz accountAuthorizer
	authz        *service.Authz
}

func NewWorkspaceMCPHandler(
	wsSvc *service.WorkspaceService,
	acctSvc *service.ClaudeAccountService,
	registry *service.ClaudeMCPRegistry,
	detector *service.WorkspaceMCPDetector,
	accountAuthz accountAuthorizer,
	authz *service.Authz,
) *WorkspaceMCPHandler {
	return &WorkspaceMCPHandler{
		wsSvc:        wsSvc,
		acctSvc:      acctSvc,
		registry:     registry,
		detector:     detector,
		accountAuthz: accountAuthz,
		authz:        authz,
	}
}

func (h *WorkspaceMCPHandler) caller(c *gin.Context) (int64, string) {
	idVal, _ := c.Get("auth_user_id")
	roleVal, _ := c.Get("auth_role")
	uid, _ := idVal.(int64)
	role, _ := roleVal.(string)
	return uid, role
}

// resolveConfigDir maps a claude account ID to its isolated configDir.
// id == 0 → "" (default account / $HOME).
func (h *WorkspaceMCPHandler) resolveConfigDir(ctx context.Context, accountID int64) string {
	if accountID == 0 {
		return ""
	}
	acc, err := h.acctSvc.GetByID(ctx, accountID)
	if err != nil || acc == nil {
		return ""
	}
	return acc.ConfigDir
}

// ListAvailable — GET /api/claude-accounts/:id/mcp/available
func (h *WorkspaceMCPHandler) ListAvailable(c *gin.Context) {
	uid, role := h.caller(c)
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad account id"})
		return
	}
	if _, err := h.accountAuthz.CanAccessClaudeAccount(c.Request.Context(), service.NewCallerInfo(uid, role), accountID); err != nil {
		h.respondAccountAuthzError(c, err)
		return
	}
	configDir := h.resolveConfigDir(c.Request.Context(), accountID)
	items, err := h.registry.List(configDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// DetectRequest is the body of POST /api/workspaces/mcp/detect.
type DetectRequest struct {
	ClaudeAccountID int64   `json:"claude_account_id"`
	RepoIDs         []int64 `json:"repo_ids"`
}

// Detect — POST /api/workspaces/mcp/detect
func (h *WorkspaceMCPHandler) Detect(c *gin.Context) {
	uid, role := h.caller(c)
	var req DetectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.ClaudeAccountID != 0 {
		if _, err := h.accountAuthz.CanAccessClaudeAccount(c.Request.Context(), service.NewCallerInfo(uid, role), req.ClaudeAccountID); err != nil {
			h.respondAccountAuthzError(c, err)
			return
		}
	}
	repoPaths, err := h.wsSvc.ResolveRepoPathsForUser(c.Request.Context(), uid, req.RepoIDs)
	if err != nil {
		// ResolveRepoPathsForUser can fail for authz OR DB errors. 403 on a
		// DB error misleads operators; map by error kind.
		switch {
		case errors.Is(err, service.ErrNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "repo not found"})
		case errors.Is(err, service.ErrForbidden):
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}
	configDir := h.resolveConfigDir(c.Request.Context(), req.ClaudeAccountID)
	res, err := h.detector.Detect(repoPaths, configDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

// Get — GET /api/workspaces/:id/mcp
func (h *WorkspaceMCPHandler) Get(c *gin.Context) {
	uid, _ := h.caller(c)
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || wsID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad workspace id"})
		return
	}
	if _, err := h.authz.CanAccessWorkspace(c.Request.Context(), uid, wsID); err != nil {
		h.respondWorkspaceAuthzError(c, err)
		return
	}
	ws, err := h.wsSvc.Get(c.Request.Context(), wsID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var accountID int64
	if ws.ClaudeAccountID.Valid {
		accountID = ws.ClaudeAccountID.Int64
	}
	configDir := h.resolveConfigDir(c.Request.Context(), accountID)
	available, err := h.registry.List(configDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	servers, err := decodeMCPServers(ws.McpServers)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "workspace mcp_servers corrupt: " + err.Error()})
		return
	}
	known := make(map[string]bool, len(available))
	conflicts := make([]service.PluginConflictInfo, 0)
	for _, k := range available {
		known[k.Name] = true
		if k.Source == service.MCPSourcePlugin {
			conflicts = append(conflicts, service.PluginConflictInfo{
				MCPName:    k.Name,
				PluginName: k.PluginName,
				MessageKey: "mcp.plugin_conflict.global_load",
			})
		}
	}
	unavailable := make([]string, 0)
	for _, name := range servers {
		if !known[name] {
			unavailable = append(unavailable, name)
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"servers":          servers,
		"unavailable":      unavailable,
		"available":        available,
		"plugin_conflicts": conflicts,
		"strict":           ws.StrictMcpConfig == 1,
	})
}

// PutMCPRequest is the body of PUT /api/workspaces/:id/mcp.
type PutMCPRequest struct {
	Servers []string `json:"servers"`
}

// Put — PUT /api/workspaces/:id/mcp
func (h *WorkspaceMCPHandler) Put(c *gin.Context) {
	uid, _ := h.caller(c)
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || wsID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad workspace id"})
		return
	}
	if _, err := h.authz.CanAccessWorkspace(c.Request.Context(), uid, wsID); err != nil {
		h.respondWorkspaceAuthzError(c, err)
		return
	}
	var req PutMCPRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Servers == nil {
		req.Servers = []string{}
	}
	res, err := h.wsSvc.UpdateMCPServers(c.Request.Context(), wsID, req.Servers)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"written":         true,
		"written_servers": res.WrittenServers,
		"unavailable":     res.Unavailable,
	})
}

// Redetect — POST /api/workspaces/:id/mcp/redetect
func (h *WorkspaceMCPHandler) Redetect(c *gin.Context) {
	uid, _ := h.caller(c)
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || wsID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad workspace id"})
		return
	}
	if _, err := h.authz.CanAccessWorkspace(c.Request.Context(), uid, wsID); err != nil {
		h.respondWorkspaceAuthzError(c, err)
		return
	}
	ws, err := h.wsSvc.Get(c.Request.Context(), wsID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var accountID int64
	if ws.ClaudeAccountID.Valid {
		accountID = ws.ClaudeAccountID.Int64
	}
	configDir := h.resolveConfigDir(c.Request.Context(), accountID)
	repoPaths, err := h.wsSvc.ResolveWorkspaceRepoPaths(c.Request.Context(), wsID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	res, err := h.detector.Detect(repoPaths, configDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

func (h *WorkspaceMCPHandler) respondAccountAuthzError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrAccountNotFound), errors.Is(err, service.ErrNotVisible):
		c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}

func (h *WorkspaceMCPHandler) respondWorkspaceAuthzError(c *gin.Context, err error) {
	// 404 on missing/invisible, 403 on found-but-forbidden — mirror the
	// pattern other niuniu workspace handlers use. We don't leak existence
	// by responding 404 for the invisible case as well.
	switch {
	case errors.Is(err, service.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "workspace not found"})
	case errors.Is(err, service.ErrForbidden):
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}

// decodeMCPServers parses the JSON-as-string mcp_servers column.
// Returns (nil, err) when the persisted blob is malformed so the caller can
// surface a 500 instead of silently "reconciling" the user's selection to []
// (which a subsequent PUT would then commit, destroying the on-disk record).
func decodeMCPServers(raw string) ([]string, error) {
	out := []string{}
	if raw == "" || raw == "null" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}

// PutStrictRequest is the body of PUT /api/workspaces/:id/mcp/strict.
type PutStrictRequest struct {
	Strict bool `json:"strict"`
}

// PutStrict — PUT /api/workspaces/:id/mcp/strict
func (h *WorkspaceMCPHandler) PutStrict(c *gin.Context) {
	uid, _ := h.caller(c)
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || wsID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad workspace id"})
		return
	}
	if _, err := h.authz.CanAccessWorkspace(c.Request.Context(), uid, wsID); err != nil {
		h.respondWorkspaceAuthzError(c, err)
		return
	}
	var req PutStrictRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.wsSvc.SetStrictMCP(c.Request.Context(), wsID, req.Strict); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"strict": req.Strict, "restart_required": true})
}
