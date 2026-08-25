package api

import (
	"database/sql"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// ProviderUsageHandler serves subscription-platform grain usage reporting:
// hourly token consumption per platform plus the rate-limit episode log.
// Owner-scoped only — a platform's quota is consumed across every workspace
// bound to it, so per-workspace numbers would not answer "how much of this
// plan have we used".
type ProviderUsageHandler struct {
	Svc   *service.ProviderUsageService
	Authz *service.Authz
	DB    *sql.DB
}

// resolveOwner validates the ?owner= filter and the caller's read access to it,
// writing the error response itself. ok=false means a response was already sent.
func (h *ProviderUsageHandler) resolveOwner(c *gin.Context) (ownerType string, ownerID int64, ok bool) {
	ownerF, err := ParseOwnerFilter(c, h.DB)
	if err != nil {
		BadRequest(c, err.Error())
		return "", 0, false
	}
	if ownerF.Type == "" {
		BadRequest(c, "owner query param is required (user:<id> or org:<slug>)")
		return "", 0, false
	}
	if userID := c.GetInt64("auth_user_id"); userID > 0 && h.Authz != nil {
		if err := h.Authz.EnsureOwnerReadable(c.Request.Context(), userID,
			service.OwnerRef{Type: ownerF.Type, ID: ownerF.ID}); err != nil {
			writeAuthzError(c, err)
			return "", 0, false
		}
	}
	return ownerF.Type, ownerF.ID, true
}

// Usage handles GET /api/provider-usage?owner=user:<id>&from=&to=.
//
// Returns the hourly per-platform series AND the per-platform totals in one
// response: the UI renders both together (chart + summary table), and splitting
// them would double the round trips for a single view while letting the two
// halves disagree about the window.
func (h *ProviderUsageHandler) Usage(c *gin.Context) {
	ownerType, ownerID, ok := h.resolveOwner(c)
	if !ok {
		return
	}
	from, to, err := parseUsageRange(c)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	ctx := c.Request.Context()
	buckets, err := h.Svc.HourlySeries(ctx, ownerType, ownerID, from, to)
	if err != nil {
		InternalError(c, err)
		return
	}
	totals, err := h.Svc.Totals(ctx, ownerType, ownerID, from, to)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"buckets": buckets,
		"totals":  totals,
		"from":    from.UTC(),
		"to":      to.UTC(),
	})
}

// RateLimitEvents handles GET /api/provider-usage/rate-limits?owner=&from=&to=.
func (h *ProviderUsageHandler) RateLimitEvents(c *gin.Context) {
	ownerType, ownerID, ok := h.resolveOwner(c)
	if !ok {
		return
	}
	from, to, err := parseUsageRange(c)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	events, err := h.Svc.Episodes(c.Request.Context(), ownerType, ownerID, from, to)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"events": events})
}
