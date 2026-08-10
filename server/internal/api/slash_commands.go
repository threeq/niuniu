package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

type SlashCommandHandler struct {
	svc           *service.SlashCommandService
	q             *store.Queries
	claudeAccount *service.ClaudeAccountService
	authz         *service.Authz
}

func NewSlashCommandHandler(
	svc *service.SlashCommandService,
	q *store.Queries,
	claudeAccount *service.ClaudeAccountService,
	authz *service.Authz,
) *SlashCommandHandler {
	return &SlashCommandHandler{
		svc:           svc,
		q:             q,
		claudeAccount: claudeAccount,
		authz:         authz,
	}
}

// ListCommands returns all available slash commands (builtins + plugins + skills).
func (h *SlashCommandHandler) ListCommands(c *gin.Context) {
	// Optional: workspace-specific agent command override
	agentCommand := c.Query("agent_command")
	if c.Query("cli_type") == "codex" && agentCommand == "" {
		agentCommand = "codex"
	}

	configDir, ok := h.resolveClaudeConfigDir(c)
	if !ok {
		return
	}

	commands, err := h.svc.ListCommands(c.Request.Context(), agentCommand, configDir)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"commands": commands})
}

func (h *SlashCommandHandler) resolveClaudeConfigDir(c *gin.Context) (string, bool) {
	raw := c.Query("workspace_id")
	if raw == "" || c.Query("cli_type") == "codex" {
		return "", true
	}
	wsID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || wsID <= 0 {
		BadRequest(c, "workspace_id must be a positive integer")
		return "", false
	}
	userID := c.GetInt64("auth_user_id")
	if h.authz != nil && userID > 0 {
		if _, err := h.authz.CanAccessWorkspace(c.Request.Context(), userID, wsID); err != nil {
			writeAuthzError(c, err)
			return "", false
		}
	}
	if h.q == nil || h.claudeAccount == nil {
		return "", true
	}
	ws, err := h.q.GetWorkspace(c.Request.Context(), wsID)
	if err != nil {
		InternalError(c, err)
		return "", false
	}
	if !ws.ClaudeAccountID.Valid || ws.ClaudeAccountID.Int64 == 0 {
		return "", true
	}
	acc, err := h.claudeAccount.GetByID(c.Request.Context(), ws.ClaudeAccountID.Int64)
	if err != nil {
		InternalError(c, err)
		return "", false
	}
	return acc.ConfigDir, true
}
