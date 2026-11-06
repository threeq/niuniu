package api

// Legacy project-level configuration for the email / generic webhook bindings
// (Epic #755 / design §6.10). These routes are kept as thin aliases of the
// IntakeSourceService management API (design §10.1) so older clients keep a
// working URL; new clients use /intake/sources.
//
// The one delivery-contract change is deliberate: like the new API, the secret
// is returned as a one-time link (`secret_link`), never as plaintext, so an
// alias caller cannot pull a secret into a conversation or client history.

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// ListWebhookBindings serves GET /api/projects/:id/intake/webhook.
func (h *IntakeHandler) ListWebhookBindings(c *gin.Context) {
	pid, ok := h.intakeProjectAccess(c, false)
	if !ok {
		return
	}
	if !h.intakeSourcesWired(c) {
		return
	}
	list, err := h.Sources.List(c.Request.Context(), c.GetInt64("auth_user_id"), pid)
	if err != nil {
		h.mapSourceErr(c, err)
		return
	}
	bindings := make([]service.IntakeSourceDTO, 0, len(list))
	for _, s := range list {
		if isLegacyWebhookKind(s.SourceKind) {
			bindings = append(bindings, s)
		}
	}
	c.JSON(http.StatusOK, gin.H{"bindings": bindings, "sources": bindings})
}

type createWebhookBindingBody struct {
	SourceKind string `json:"source_kind" binding:"required"`
	Name       string `json:"name" binding:"required"`
}

// CreateWebhookBinding serves POST /api/projects/:id/intake/webhook. The
// generated secret is delivered through the one-time link, exactly like the new
// API; the legacy surface never returns it inline.
func (h *IntakeHandler) CreateWebhookBinding(c *gin.Context) {
	pid, ok := h.intakeProjectAccess(c, true)
	if !ok {
		return
	}
	if !h.intakeSourcesWired(c) {
		return
	}
	var body createWebhookBindingBody
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, "source_kind and name are required")
		return
	}
	kind := strings.ToLower(strings.TrimSpace(body.SourceKind))
	if !isLegacyWebhookKind(kind) {
		BadRequest(c, "source_kind must be email or webhook")
		return
	}
	// The legacy route has no mode field; email was always the inbound parser.
	mode := "push"
	if kind == "email" {
		mode = "inbound"
	}
	dto, secret, err := h.Sources.Create(c.Request.Context(), service.IntakeSourceInput{
		ProjectID:    pid,
		SourceKind:   kind,
		Name:         body.Name,
		Mode:         mode,
		CallerUserID: c.GetInt64("auth_user_id"),
	})
	if err != nil {
		h.mapSourceErr(c, err)
		return
	}
	resp := gin.H{"binding": dto, "source": dto, "secret_shown": false}
	if secret != "" {
		link, err := h.issueSecretLink(*dto, secret)
		if err != nil {
			h.mapSourceErr(c, err)
			return
		}
		resp["secret_link"] = link
	}
	c.JSON(http.StatusCreated, resp)
}

// DeleteWebhookBinding serves DELETE /api/projects/:id/intake/webhook/:bid.
func (h *IntakeHandler) DeleteWebhookBinding(c *gin.Context) {
	pid, ok := h.intakeProjectAccess(c, true)
	if !ok {
		return
	}
	if !h.intakeSourcesWired(c) {
		return
	}
	bid, err := strconv.ParseInt(strings.TrimSpace(c.Param("bid")), 10, 64)
	if err != nil || bid <= 0 {
		BadRequest(c, "invalid binding ID")
		return
	}
	if err := h.Sources.Delete(c.Request.Context(), c.GetInt64("auth_user_id"), pid, bid); err != nil {
		h.mapSourceErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
