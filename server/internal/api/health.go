package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/agentproxy"
)

// Version is set at build time via -ldflags.
var Version = "dev"

type HealthHandler struct {
	startedAt   time.Time
	serverID    string
	authEnabled bool
}

func NewHealthHandler(serverID string, authEnabled bool) *HealthHandler {
	return &HealthHandler{
		startedAt:   time.Now(),
		serverID:    serverID,
		authEnabled: authEnabled,
	}
}

func (h *HealthHandler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":         "ok",
		"version":        Version,
		"uptime_seconds": int(time.Since(h.startedAt).Seconds()),
		"server_id":      h.serverID,
		"auth_enabled":   h.authEnabled,
		"api_version":    1,
		"autohost": gin.H{
			"llm_judge": gin.H{
				"cli_available": agentproxy.ClaudeCLIAvailable(),
			},
		},
	})
}
