package api

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// MCPPermissionHandler serves /mcp/permission-prompt — the niuniu-mcp shim
// posts here on every Claude CLI tool call gated by --permission-prompt-tool.
// The handler delegates to PermissionService.Request, which either
// short-circuits via the allowlist or blocks until the user answers in chat.
type MCPPermissionHandler struct {
	Perm *service.PermissionService
	Q    *store.Queries
}

// Prompt is POST /mcp/permission-prompt.
//
// Request body: {"tool_name": "...", "tool_input": {...}}
// Response body: AllowResp ({"behavior": "allow"|"deny", ...})
//
// MCPTokenAuth has already validated the bearer token and set
// `mcp_workspace_id` on the gin.Context. We resolve owner_type / owner_id /
// session_id by reading the workspace row (the middleware doesn't propagate
// those — see api/mcp_token.go).
func (h *MCPPermissionHandler) Prompt(c *gin.Context) {
	var body struct {
		ToolName  string         `json:"tool_name"`
		ToolInput map[string]any `json:"tool_input"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if body.ToolName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tool_name required"})
		return
	}

	wsID := c.GetInt64("mcp_workspace_id")
	if wsID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "no workspace context"})
		return
	}

	ws, err := h.Q.GetWorkspace(c.Request.Context(), wsID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "workspace lookup: " + err.Error()})
		return
	}
	sessionID := ws.SessionID.String

	resp, err := h.Perm.Request(c.Request.Context(),
		wsID, ws.OwnerType, ws.OwnerID, sessionID,
		body.ToolName, body.ToolInput)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out, _ := json.Marshal(resp)
	c.Data(http.StatusOK, "application/json", out)
}
