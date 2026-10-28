package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// QueueDrainer triggers a drain of every owner that currently has queued
// workspace starts. *service.OrchestrationGuard implements it. Wired optionally
// so that raising (or removing) the concurrency cap immediately uses the freed
// capacity, instead of leaving already-queued issues to wait for the next
// workspace-completion event (which a settings change does not itself produce).
type QueueDrainer interface {
	DrainAllQueuedOwners(ctx context.Context)
}

// adminSettingSpec describes a whitelisted, admin-mutable server setting. The
// whitelist is intentional: any key not present here is rejected as 404 so
// arbitrary key/value injection through the admin endpoint is impossible.
// Kind selects how GET reads the value back (int settings go through GetInt
// with the spec default; string settings through GetString).
type adminSettingSpec struct {
	Validate func(string) error
	Default  string
	Kind     adminSettingKind
}

type adminSettingKind int

const (
	adminSettingInt adminSettingKind = iota
	adminSettingString
)

// adminSettingKeys is the whitelist of admin-settable server keys. New keys
// must be added here explicitly together with their validator.
var adminSettingKeys = map[string]adminSettingSpec{
	// Orchestration cost guardrails (spec 2026-06-08): runtime-tunable from the
	// settings page. Stored as ints on the existing GetInt rail; budget is whole
	// USD, warn ratio is whole percent 0..100 (guard divides by 100). 0 disables
	// the respective guardrail. Defaults mirror config viper defaults.
	"orchestration.chain_cost_budget_usd": {
		Validate: nonNegativeInt,
		Default:  "10",
	},
	"orchestration.max_concurrent_workspaces": {
		Validate: nonNegativeInt,
		Default:  "5",
	},
	"orchestration.max_batch_issues": {
		Validate: nonNegativeInt,
		Default:  "20",
	},
	"orchestration.chain_cost_warn_ratio": {
		Validate: func(v string) error {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 || n > 100 {
				return errors.New("must be integer 0..100")
			}
			return nil
		},
		Default: "80",
	},
	// Workspace share access domain (团队版)：share links are built as
	// <base>/share/ws/<token>. Empty = unconfigured (frontend prefixes origin).
	// Accepts a bare host ("share.example.com") or a full origin with scheme
	// ("https://share.example.com"), optional port and optional sub-path; no
	// query/fragment/userinfo.
	"share.base_url": {
		Validate: validateShareBaseURL,
		Default:  "",
		Kind:     adminSettingString,
	},
	// Global workspace expiry window in days. The hourly sweeper deletes any
	// workspace (and its linked issue) whose last activity is older than this.
	// 0 = disabled (default). Applied on the next sweep, no restart needed.
	service.KeyWorkspaceExpireDays: {
		Validate: nonNegativeInt,
		Default:  "0",
	},
}

// validateShareBaseURL checks the share access domain. Empty is allowed
// (= unconfigured). Otherwise it must parse as a URL with a host, http(s)
// scheme (or none), and no query/fragment/userinfo.
func validateShareBaseURL(v string) error {
	if v == "" {
		return nil
	}
	u, err := url.Parse(v)
	if err != nil {
		return errors.New("must be a valid URL")
	}
	if u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("scheme must be http or https")
	}
	if u.Host == "" {
		return errors.New("host is required")
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return errors.New("query, fragment and userinfo are not allowed")
	}
	return nil
}

// nonNegativeInt validates a whitelisted setting that must parse as an integer >= 0.
func nonNegativeInt(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return errors.New("must be integer >= 0")
	}
	return nil
}

// AdminSettingsHandler serves GET/PUT /api/admin/settings/:key. Route guard
// (auth.RequireAdmin) is applied at the router level, not here.
type AdminSettingsHandler struct {
	svc     *service.ServerSettingsService
	drainer QueueDrainer // optional; nil-safe (no post-write drain when unset)
}

// NewAdminSettingsHandler wires the ServerSettingsService dependency. drainer is
// optional (may be nil): when set, raising the concurrency cap triggers an
// immediate queue drain so freed capacity is used without waiting for the next
// workspace-completion event.
func NewAdminSettingsHandler(svc *service.ServerSettingsService, drainer QueueDrainer) *AdminSettingsHandler {
	return &AdminSettingsHandler{svc: svc, drainer: drainer}
}

// adminSettingResponse is the JSON shape returned by both GET and PUT.
type adminSettingResponse struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// GetSetting returns the current value of a whitelisted server setting.
// Falls back to the spec's Default when the key has not been written yet.
func (h *AdminSettingsHandler) GetSetting(c *gin.Context) {
	key := c.Param("key")
	spec, ok := adminSettingKeys[key]
	if !ok {
		NotFound(c, "SETTING")
		return
	}
	if spec.Kind == adminSettingString {
		v := h.svc.GetString(c.Request.Context(), key, spec.Default)
		c.JSON(http.StatusOK, adminSettingResponse{Key: key, Value: v})
		return
	}
	defInt, _ := strconv.Atoi(spec.Default)
	v := h.svc.GetInt(c.Request.Context(), key, defInt)
	c.JSON(http.StatusOK, adminSettingResponse{Key: key, Value: strconv.Itoa(v)})
}

type putSettingReq struct {
	Value string `json:"value"`
}

// PutSetting validates and persists a new value for a whitelisted setting,
// recording the caller's user id for audit.
func (h *AdminSettingsHandler) PutSetting(c *gin.Context) {
	key := c.Param("key")
	spec, ok := adminSettingKeys[key]
	if !ok {
		NotFound(c, "SETTING")
		return
	}
	var req putSettingReq
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "invalid request body")
		return
	}
	if err := spec.Validate(req.Value); err != nil {
		BadRequest(c, err.Error())
		return
	}
	userID := c.GetInt64("auth_user_id")
	if err := h.svc.Put(c.Request.Context(), key, req.Value, userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{"code": "INTERNAL", "message": err.Error()},
		})
		return
	}
	// Raising (or removing) the per-owner concurrency cap frees capacity that the
	// drain only revisits on a workspace-completion event — which a settings change
	// does not produce. Trigger a one-shot global drain so already-queued issues
	// start now. Runs detached (it may create workspaces) with a background context
	// so it outlives this request. Lowering the cap is harmless: the drain finds no
	// free slot and returns immediately. Best-effort; not part of the PUT result.
	if key == service.KeyOrchMaxConcurrent && h.drainer != nil {
		drainer := h.drainer
		go drainer.DrainAllQueuedOwners(context.Background())
	}
	c.JSON(http.StatusOK, adminSettingResponse{Key: key, Value: req.Value})
}
