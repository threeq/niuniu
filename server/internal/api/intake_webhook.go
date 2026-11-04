package api

// Project-level configuration for the team-edition email / generic webhook
// Sources (Epic #755 / design §6.10). The secret is shown exactly once, at
// creation; list responses report only whether a secret is configured.

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// ListWebhookBindings serves GET /api/projects/:id/intake/webhook.
func (h *IntakeHandler) ListWebhookBindings(c *gin.Context) {
	pid, ok := h.intakeProjectAccess(c, false)
	if !ok {
		return
	}
	if h.Webhook == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "webhook source is not wired"})
		return
	}
	bindings, err := h.Webhook.ListWebhookBindings(c.Request.Context(), pid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"bindings": bindings})
}

type createWebhookBindingBody struct {
	SourceKind string `json:"source_kind" binding:"required"`
	Name       string `json:"name" binding:"required"`
}

// CreateWebhookBinding serves POST /api/projects/:id/intake/webhook. The
// response carries the generated secret ONCE; it is never readable again.
func (h *IntakeHandler) CreateWebhookBinding(c *gin.Context) {
	pid, ok := h.intakeProjectAccess(c, true)
	if !ok {
		return
	}
	if h.Webhook == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "webhook source is not wired"})
		return
	}
	var body createWebhookBindingBody
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, "source_kind and name are required")
		return
	}
	uid := c.GetInt64("auth_user_id")
	binding, secret, err := h.Webhook.CreateWebhookBinding(
		c.Request.Context(), pid, uid, strings.TrimSpace(body.SourceKind), body.Name)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"binding":      binding,
		"secret":       secret,
		"secret_shown": true,
	})
}

// DeleteWebhookBinding serves DELETE /api/projects/:id/intake/webhook/:bid.
func (h *IntakeHandler) DeleteWebhookBinding(c *gin.Context) {
	pid, ok := h.intakeProjectAccess(c, true)
	if !ok {
		return
	}
	if h.Webhook == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "webhook source is not wired"})
		return
	}
	bid, err := strconv.ParseInt(strings.TrimSpace(c.Param("bid")), 10, 64)
	if err != nil || bid <= 0 {
		BadRequest(c, "invalid binding ID")
		return
	}
	if err := h.Webhook.DeleteWebhookBinding(c.Request.Context(), pid, bid); err != nil {
		h.mapIntakeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
