package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/terminal"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: checkWSOrigin,
}

type AgentHandler struct {
	agentMgr     *service.AgentManager
	workspaceSvc *service.WorkspaceService
	Authz        *service.Authz
}

func NewAgentHandler(agentMgr *service.AgentManager, workspaceSvc *service.WorkspaceService) *AgentHandler {
	return &AgentHandler{
		agentMgr:     agentMgr,
		workspaceSvc: workspaceSvc,
	}
}

type StartAgentRequest struct {
	Prompt string `json:"prompt"`
}

type SendInputRequest struct {
	Input string `json:"input" binding:"required"`
}

// Start starts an agent for a workspace
// @Summary      Start an agent
// @Description  Start an agent for a workspace with optional prompt
// @Tags         Agents
// @Accept       json
// @Produce      json
// @Param        id       path      string                true  "Workspace ID"
// @Param        request  body      StartAgentRequest  false  "Agent start request"
// @Success      204
// @Failure      404     {object}  Error
// @Failure      500     {object}  Error
// @Router       /workspaces/{id}/agent/start [post]
func (h *AgentHandler) Start(c *gin.Context) {
	workspaceIDStr := c.Param("id")
	workspaceID, err := strconv.ParseInt(workspaceIDStr, 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}

	if err := h.workspaceSvc.CheckNotArchived(c.Request.Context(), workspaceID); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}

	var req StartAgentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// Prompt is optional, ignore error
		req.Prompt = ""
	}

	// Get workspace to get workDir
	workspace, err := h.workspaceSvc.Get(c.Request.Context(), workspaceID)
	if err != nil {
		slog.Warn("StartAgent failed to get workspace", "workspaceID", workspaceID, "error", err)
		NotFound(c, "WORKSPACE")
		return
	}

	// Start agent
	userID := c.GetInt64("auth_user_id")
	if err := h.agentMgr.Start(c.Request.Context(), workspaceID, workspace.Path, req.Prompt, userID); err != nil {
		InternalError(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}

// Stop stops an agent for a workspace
// @Summary      Stop an agent
// @Description  Stop an agent for a workspace
// @Tags         Agents
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Workspace ID"
// @Success      204
// @Failure      404  {object}  Error
// @Failure      500  {object}  Error
// @Router       /workspaces/{id}/agent/stop [post]
func (h *AgentHandler) Stop(c *gin.Context) {
	workspaceIDStr := c.Param("id")
	workspaceID, err := strconv.ParseInt(workspaceIDStr, 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}

	if err := h.workspaceSvc.CheckNotArchived(c.Request.Context(), workspaceID); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}

	if err := h.agentMgr.Stop(c.Request.Context(), workspaceID); err != nil {
		InternalError(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}

// Send sends input to a running agent
// @Summary      Send input to an agent
// @Description  Send input to a running agent
// @Tags         Agents
// @Accept       json
// @Produce      json
// @Param        id       path      string                true  "Workspace ID"
// @Param        request  body      SendInputRequest  true  "Input to send"
// @Success      204
// @Failure      400     {object}  Error
// @Failure      404     {object}  Error
// @Failure      500     {object}  Error
// @Router       /workspaces/{id}/agent/send [post]
func (h *AgentHandler) Send(c *gin.Context) {
	workspaceIDStr := c.Param("id")
	workspaceID, err := strconv.ParseInt(workspaceIDStr, 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}

	var req SendInputRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}

	if err := h.agentMgr.Send(workspaceID, req.Input); err != nil {
		InternalError(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}

// Status returns the status of an agent
// @Summary      Get agent status
// @Description  Get the current status of an agent
// @Tags         Agents
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Workspace ID"
// @Success      200  {object}  map[string]string
// @Failure      500  {object}  Error
// @Router       /workspaces/{id}/agent/status [get]
func (h *AgentHandler) Status(c *gin.Context) {
	workspaceIDStr := c.Param("id")
	workspaceID, err := strconv.ParseInt(workspaceIDStr, 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}

	status := h.agentMgr.Status(workspaceID)
	c.JSON(http.StatusOK, gin.H{"status": status})
}

// Terminal provides a WebSocket connection to the agent's terminal
// @Summary      Agent terminal WebSocket
// @Description  Establish a WebSocket connection to the agent's terminal
// @Tags         Agents
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Workspace ID"
// @Success      101
// @Failure      404  {object}  Error
// @Failure      500  {object}  Error
// @Router       /ws/workspaces/{id}/terminal [get]
func (h *AgentHandler) Terminal(c *gin.Context) {
	workspaceIDStr := c.Param("id")
	workspaceID, err := strconv.ParseInt(workspaceIDStr, 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}

	// Authz subscribe-time gate: reject the upgrade if the caller cannot access
	// this workspace. Must happen before wsUpgrade so we can still send HTTP 403.
	if h.Authz != nil {
		userID := c.GetInt64("auth_user_id")
		if userID != 0 {
			if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, workspaceID); err != nil {
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
		}
	}

	var proc *terminal.PTYProcess
	var cleanup func()

	// Try to get existing agent process first
	proc, exists := h.agentMgr.GetProcess(workspaceID)
	if exists {
		// Agent is running, register a terminal connection for it
		cleanup = h.agentMgr.RegisterTerminalConnection(workspaceID)
	} else {
		// No agent running - start a terminal shell for the workspace instead
		// StartTerminal already registers a connection and returns cleanup
		proc, cleanup, err = h.agentMgr.StartTerminal(c.Request.Context(), workspaceID)
		if err != nil {
			slog.Warn("StartTerminal failed", "workspace_id", workspaceID, "error", err)
			RespondError(c, http.StatusNotFound, "WORKSPACE_NOT_FOUND", fmt.Sprintf("Workspace not found: %v", err))
			return
		}
	}

	// Upgrade HTTP to WebSocket
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		cleanup()
		InternalError(c, err)
		return
	}

	// Bridge WebSocket and PTY process with connection tracking
	// cleanup will be called by onClose when the WebSocket disconnects
	if err := terminal.Bridge(conn, proc, func() bool {
		cleanup()
		return false // Bridge already called cleanup via onClose
	}); err != nil {
		slog.Warn("terminal bridge error", "error", err)
	}
}
