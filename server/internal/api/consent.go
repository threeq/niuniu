package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/consent"
)

// ConsentHandler serves the current user's privacy & disclaimer consent state
// and records acceptance.
type ConsentHandler struct {
	svc *consent.Service
}

func NewConsentHandler(svc *consent.Service) *ConsentHandler {
	return &ConsentHandler{svc: svc}
}

// GetStatus returns the consent state for the authenticated caller. Drives the
// SPA consent gate. Read-only, so it passes both the license and consent
// guards even when those are locked.
func (h *ConsentHandler) GetStatus(c *gin.Context) {
	uid, ok := callerID(c)
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"code": "UNAUTHORIZED", "message": "missing identity"},
		})
		return
	}
	st, err := h.svc.Status(c.Request.Context(), uid)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, st)
}

// Accept records the caller's acceptance of the submitted agreement version.
func (h *ConsentHandler) Accept(c *gin.Context) {
	uid, ok := callerID(c)
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"code": "UNAUTHORIZED", "message": "missing identity"},
		})
		return
	}
	var req struct {
		Version string `json:"version" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "version is required")
		return
	}
	if err := h.svc.Accept(c.Request.Context(), uid, req.Version); err != nil {
		if err == consent.ErrVersionMismatch {
			RespondError(c, http.StatusBadRequest, "CONSENT_VERSION_MISMATCH",
				"agreement version is out of date, please reload")
			return
		}
		InternalError(c, err)
		return
	}
	st, err := h.svc.Status(c.Request.Context(), uid)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, st)
}
