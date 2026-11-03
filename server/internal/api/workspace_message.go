package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// WorkspaceMessageHandler backs the cross-workspace agent messaging tools
// (list_workspaces / send_workspace_message).
//
// The SENDER is always the workspace the MCP bearer token belongs to
// (mcp_workspace_id, set by MCPTokenAuth) — never a request-body field, so one
// workspace cannot impersonate another. Peers are restricted to the sender's
// own project by the service.
type WorkspaceMessageHandler struct {
	Svc *service.WorkspaceMessageService
}

// ListPeers handles GET /mcp/peer-workspaces.
func (h *WorkspaceMessageHandler) ListPeers(c *gin.Context) {
	senderID := c.GetInt64("mcp_workspace_id")
	if senderID == 0 {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"code": "WORKSPACE_UNBOUND", "message": "MCP token is not bound to a workspace"},
		})
		return
	}
	peers, err := h.Svc.List(c.Request.Context(), senderID)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": peers})
}

// SendMessage handles POST /mcp/workspace-messages/send.
// Body: {target_workspace_id?, target_issue_id?, text, reply_to?}
func (h *WorkspaceMessageHandler) SendMessage(c *gin.Context) {
	senderID := c.GetInt64("mcp_workspace_id")
	if senderID == 0 {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"code": "WORKSPACE_UNBOUND", "message": "MCP token is not bound to a workspace"},
		})
		return
	}
	var req struct {
		TargetWorkspaceID int64  `json:"target_workspace_id"`
		TargetIssueID     int64  `json:"target_issue_id"`
		Text              string `json:"text"`
		ReplyTo           string `json:"reply_to"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	res, err := h.Svc.Send(c.Request.Context(), senderID, service.WorkspaceMessageRequest{
		TargetWorkspaceID: req.TargetWorkspaceID,
		TargetIssueID:     req.TargetIssueID,
		Text:              req.Text,
		ReplyTo:           req.ReplyTo,
	})
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":       "sent",
		"message_id":   res.MessageID,
		"workspace_id": res.WorkspaceID,
		"queued":       res.Queued,
	})
}
