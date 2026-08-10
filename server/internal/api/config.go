package api

import (
	"context"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/config"
)

// serverSettingsReader is the read-only slice of *service.ServerSettingsService
// the config snapshot needs to surface admin-toggled capability flags (e.g.
// assistant_enabled). Kept as an interface so unit tests can inject a fake
// without a DB.
type serverSettingsReader interface {
	GetInt(ctx context.Context, key string, def int) int
}

// ConfigHandler serves GET/PUT /api/config. It owns the live *config.Config
// and a Save hook. In production Save is config.Save (writes ~/.niuniu/config.yaml);
// tests inject an in-memory Save so the GET/PUT round-trip can be exercised
// without touching the user's real config file.
type ConfigHandler struct {
	Cfg  *config.Config
	Save func(*config.Config) error

	// OnTelemetryToggle, when set, is invoked with the new value whenever
	// telemetry_enabled changes via PUT. server.New wires it to the running
	// telemetry reporter's SetEnabled so flipping the opt-out in Settings stops
	// or resumes reporting without a restart (#366 -> #365). nil on team edition
	// (no reporter) and in handler unit tests that don't exercise the wire-up.
	OnTelemetryToggle func(bool)

	// Settings backs read-only capability flags exposed in the snapshot (e.g.
	// assistant_enabled, which admins toggle via /admin/settings). nil in unit
	// tests → flags read as their zero/false default.
	Settings serverSettingsReader
}

// NewConfigHandler wires the handler with the production config.Save persister.
func NewConfigHandler(cfg *config.Config) *ConfigHandler {
	return &ConfigHandler{Cfg: cfg, Save: config.Save}
}

// assistantEnabled reports whether the 牛牛助手 nav entry capability is switched
// on. Read-only mirror of the admin-settable keyAssistantEnabled flag; false
// when no Settings backend is wired (unit tests) or the key is unset (default).
func (h *ConfigHandler) assistantEnabled(ctx context.Context) bool {
	if h.Settings == nil {
		return false
	}
	return h.Settings.GetInt(ctx, keyAssistantEnabled, 0) > 0
}

// snapshot is the shared GET/PUT response shape. telemetry_enabled is exposed
// as a top-level flag (opt-out, default true) so the SPA can read and toggle it.
func (h *ConfigHandler) snapshot(ctx context.Context) gin.H {
	return gin.H{
		"server": gin.H{
			"port": h.Cfg.Server.Port,
			"host": h.Cfg.Server.Host,
		},
		"storage": gin.H{
			"driver":      h.Cfg.Storage.Driver,
			"sqlite_path": h.Cfg.Storage.SQLite.Path,
		},
		"workspace": gin.H{
			"base_dir": h.Cfg.Workspace.BaseDir,
		},
		"agent": gin.H{
			"command": h.Cfg.Agent.ClaudeCode.Command,
			"args":    h.Cfg.Agent.ClaudeCode.Args,
		},
		"editor": gin.H{
			"vscode_mode":       h.Cfg.Editor.VSCodeMode,
			"vscode_remote_url": h.Cfg.Editor.VSCodeRemoteURL,
		},
		"telemetry_enabled": h.Cfg.Telemetry.Enabled,
		// assistant_enabled gates the 牛牛助手 nav entry in team edition (personal
		// edition always shows it). Admin-toggled via /admin/settings; read-only here.
		"assistant_enabled": h.assistantEnabled(ctx),
	}
}

// Get handles GET /api/config.
func (h *ConfigHandler) Get(c *gin.Context) {
	c.JSON(http.StatusOK, h.snapshot(c.Request.Context()))
}

// configUpdateRequest is the partial-update body for PUT /api/config. Every
// field is a pointer so an omitted section leaves the current value untouched.
type configUpdateRequest struct {
	Server *struct {
		Port *int    `json:"port"`
		Host *string `json:"host"`
	} `json:"server"`
	Workspace *struct {
		BaseDir *string `json:"base_dir"`
	} `json:"workspace"`
	Agent *struct {
		Command *string  `json:"command"`
		Args    []string `json:"args"`
	} `json:"agent"`
	Editor *struct {
		VSCodeMode      *string `json:"vscode_mode"`
		VSCodeRemoteURL *string `json:"vscode_remote_url"`
	} `json:"editor"`
	// TelemetryEnabled is the opt-out toggle (top-level, matching the GET
	// snapshot's telemetry_enabled). Nil = leave unchanged.
	TelemetryEnabled *bool `json:"telemetry_enabled"`
}

// Update handles PUT /api/config: applies the partial update, persists it, and
// echoes the updated config in the same shape as Get.
func (h *ConfigHandler) Update(c *gin.Context) {
	var req configUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"code": "BAD_REQUEST", "message": err.Error()},
		})
		return
	}

	if req.Server != nil {
		if req.Server.Port != nil {
			h.Cfg.Server.Port = *req.Server.Port
		}
		if req.Server.Host != nil {
			h.Cfg.Server.Host = *req.Server.Host
		}
	}
	if req.Workspace != nil {
		if req.Workspace.BaseDir != nil {
			h.Cfg.Workspace.BaseDir = *req.Workspace.BaseDir
		}
	}
	if req.Agent != nil {
		if req.Agent.Command != nil {
			h.Cfg.Agent.ClaudeCode.Command = *req.Agent.Command
		}
		if req.Agent.Args != nil {
			h.Cfg.Agent.ClaudeCode.Args = req.Agent.Args
		}
	}
	if req.Editor != nil {
		if req.Editor.VSCodeMode != nil {
			mode := *req.Editor.VSCodeMode
			if mode != "local" && mode != "remote" {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": gin.H{"code": "BAD_REQUEST", "message": "vscode_mode must be 'local' or 'remote'"},
				})
				return
			}
			h.Cfg.Editor.VSCodeMode = mode
		}
		if req.Editor.VSCodeRemoteURL != nil {
			rawURL := *req.Editor.VSCodeRemoteURL
			if rawURL != "" {
				u, err := url.Parse(rawURL)
				if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
					c.JSON(http.StatusBadRequest, gin.H{
						"error": gin.H{"code": "BAD_REQUEST", "message": "vscode_remote_url must be an http or https URL"},
					})
					return
				}
			}
			h.Cfg.Editor.VSCodeRemoteURL = rawURL
		}
	}
	if req.TelemetryEnabled != nil {
		h.Cfg.Telemetry.Enabled = *req.TelemetryEnabled
		// The running reporter (#365) reads a private atomic seeded at boot, NOT
		// this struct, on each tick -- so persisting the field is not enough to
		// stop a live reporter. Push the new value into it via the wired callback
		// so the opt-out takes effect immediately (next tick) without a restart.
		// nil on team edition where no reporter runs.
		if h.OnTelemetryToggle != nil {
			h.OnTelemetryToggle(*req.TelemetryEnabled)
		}
	}

	if err := h.Save(h.Cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{"code": "INTERNAL_ERROR", "message": err.Error()},
		})
		return
	}

	c.JSON(http.StatusOK, h.snapshot(c.Request.Context()))
}
