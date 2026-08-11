package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

type SlashCommandHandler struct {
	svc   *service.SlashCommandService
	authz *service.Authz
}

func NewSlashCommandHandler(svc *service.SlashCommandService, authz *service.Authz) *SlashCommandHandler {
	return &SlashCommandHandler{svc: svc, authz: authz}
}

// ListCommands returns all available slash commands (builtins + plugins + skills).
func (h *SlashCommandHandler) ListCommands(c *gin.Context) {
	agentCommand := c.Query("agent_command")
	if c.Query("cli_type") == "codex" && agentCommand == "" {
		agentCommand = "codex"
	}
	commands, err := h.svc.ListCommands(c.Request.Context(), agentCommand, "")
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"commands": commands})
}
