package api

// Public inbound webhook endpoints for the team edition (Epic #755 / design
// §6.10). They sit OUTSIDE the authenticated /api group: the per-binding
// signature (GitHub HMAC) or bearer secret IS the authorization, exactly like
// the IM public webhook. A forged or unsigned delivery gets 401 and records
// nothing; a valid one enters the shared Intake pipeline, which enforces actor
// scope, the daily cap and audit.

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/intake"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// IntegrationWebhookHandler serves the public webhook routes.
type IntegrationWebhookHandler struct {
	svc *service.WebhookSourceService
}

// NewIntegrationWebhookHandler wires the inbound service.
func NewIntegrationWebhookHandler(svc *service.WebhookSourceService) *IntegrationWebhookHandler {
	return &IntegrationWebhookHandler{svc: svc}
}

// GitHub handles POST /api/integrations/github/webhook/:binding.
func (h *IntegrationWebhookHandler) GitHub(c *gin.Context) {
	h.handle(c, intake.SourceKindGitHub)
}

// Email handles POST /api/integrations/email/:binding.
func (h *IntegrationWebhookHandler) Email(c *gin.Context) {
	h.handle(c, intake.SourceKindEmail)
}

// Webhook handles POST /api/integrations/webhook/:binding.
func (h *IntegrationWebhookHandler) Webhook(c *gin.Context) {
	h.handle(c, intake.SourceKindWebhook)
}

func (h *IntegrationWebhookHandler) handle(c *gin.Context, expectedProvider string) {
	if h.svc == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "webhook source is not wired"})
		return
	}
	bindingID, err := strconv.ParseInt(strings.TrimSpace(c.Param("binding")), 10, 64)
	if err != nil || bindingID <= 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	limit, err := h.svc.BodyLimit(c.Request.Context(), bindingID, expectedProvider)
	if err != nil {
		writeWebhookError(c, err)
		return
	}
	// Read at most one byte past the cap: enough to distinguish "exactly at the
	// limit" from "too large" without buffering an unbounded payload.
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, limit+1))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "read body failed"})
		return
	}
	if int64(len(body)) > limit {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "payload too large"})
		return
	}

	out, err := h.svc.HandleInbound(c.Request.Context(), bindingID, expectedProvider, c.Request, body)
	if err != nil {
		writeWebhookError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":          true,
		"ignored":     out.Ignored,
		"duplicate":   out.Duplicate,
		"decision":    out.Decision,
		"reason_code": out.ReasonCode,
	})
}

// writeWebhookError maps a service error to the fixed inbound status contract.
func writeWebhookError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrIntakeWebhookUnauthorized):
		// A forged delivery is rejected outright, never acknowledged as "ignored".
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
	case errors.Is(err, service.ErrWebhookNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	case errors.Is(err, service.ErrWebhookBadRequest):
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
	case errors.Is(err, service.ErrWebhookRateLimited):
		c.Header("Retry-After", "1")
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "webhook processing failed"})
	}
}
