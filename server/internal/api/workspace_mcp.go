package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/niuniu-dev/niuniu/internal/service"
)

// WorkspaceMCPHandler manages per-workspace MCP configuration. Multi-account
// switching removed: MCP ops target the host's global ~/.claude/ (configDir="").
type WorkspaceMCPHandler struct {
	wsSvc    *service.WorkspaceService
	registry *service.ClaudeMCPRegistry
	detector *service.WorkspaceMCPDetector
	authz    *service.Authz
}

func NewWorkspaceMCPHandler(
	wsSvc *service.WorkspaceService,
	registry *service.ClaudeMCPRegistry,
	detector *service.WorkspaceMCPDetector,
	authz *service.Authz,
) *WorkspaceMCPHandler {
	return &WorkspaceMCPHandler{wsSvc: wsSvc, registry: registry, detector: detector, authz: authz}
}

func (h *WorkspaceMCPHandler) caller(c *gin.Context) (int64, string) {
	idVal, _ := c.Get("auth_user_id")
	roleVal, _ := c.Get("auth_role")
	uid, _ := idVal.(int64)
	role, _ := roleVal.(string)
	return uid, role
}

type DetectRequest struct {
	RepoIDs []int64 `json:"repo_ids"`
}

func (h *WorkspaceMCPHandler) Detect(c *gin.Context) {
	uid, _ := h.caller(c)
	var req DetectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	repoPaths, err := h.wsSvc.ResolveRepoPathsForUser(c.Request.Context(), uid, req.RepoIDs)
	if err != nil {
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
	res, err := h.detector.Detect(repoPaths, "")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

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
	available, err := h.registry.List("")
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
			conflicts = append(conflicts, service.PluginConflictInfo{MCPName: k.Name, PluginName: k.PluginName, MessageKey: "mcp.plugin_conflict.global_load"})
		}
	}
	unavailable := make([]string, 0)
	for _, name := range servers {
		if !known[name] {
			unavailable = append(unavailable, name)
		}
	}
	c.JSON(http.StatusOK, gin.H{"servers": servers, "unavailable": unavailable, "available": available, "plugin_conflicts": conflicts, "strict": ws.StrictMcpConfig == 1})
}

type PutMCPRequest struct {
	Servers []string `json:"servers"`
}

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
	c.JSON(http.StatusOK, gin.H{"written": true, "written_servers": res.WrittenServers, "unavailable": res.Unavailable})
}

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
	if _, err := h.wsSvc.Get(c.Request.Context(), wsID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	repoPaths, err := h.wsSvc.ResolveWorkspaceRepoPaths(c.Request.Context(), wsID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	res, err := h.detector.Detect(repoPaths, "")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

func (h *WorkspaceMCPHandler) respondWorkspaceAuthzError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "workspace not found"})
	case errors.Is(err, service.ErrForbidden):
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}

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

type PutStrictRequest struct {
	Strict bool `json:"strict"`
}

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
