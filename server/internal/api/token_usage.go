package api

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// TokenUsageHandler serves hourly token-usage time series for a workspace
// (per-workspace grain) or an owner (summed across the owner's workspaces).
type TokenUsageHandler struct {
	Svc   *service.TokenUsageService
	Authz *service.Authz
	DB    *sql.DB
}

const maxUsageRange = 400 * 24 * time.Hour

// parseUsageRange returns [from, to). Defaults to the last 7 days; rejects
// inverted or oversized ranges to keep the scan bounded.
func parseUsageRange(c *gin.Context) (from, to time.Time, err error) {
	to = time.Now().UTC()
	from = to.Add(-7 * 24 * time.Hour)
	if v := c.Query("to"); v != "" {
		if to, err = time.Parse(time.RFC3339, v); err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid 'to' (want RFC3339)")
		}
	}
	if v := c.Query("from"); v != "" {
		if from, err = time.Parse(time.RFC3339, v); err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid 'from' (want RFC3339)")
		}
	}
	if !from.Before(to) {
		return time.Time{}, time.Time{}, fmt.Errorf("'from' must be before 'to'")
	}
	if to.Sub(from) > maxUsageRange {
		return time.Time{}, time.Time{}, fmt.Errorf("range too large (max 400 days)")
	}
	return from, to, nil
}

// WorkspaceUsage handles GET /api/workspaces/:id/token-usage.
func (h *TokenUsageHandler) WorkspaceUsage(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, wsID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	from, to, err := parseUsageRange(c)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	buckets, err := h.Svc.WorkspaceSeries(c.Request.Context(), wsID, from, to)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"buckets": buckets})
}

// OwnerUsage handles GET /api/token-usage?owner=user:<id>|org:<slug>.
func (h *TokenUsageHandler) OwnerUsage(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	ownerF, err := ParseOwnerFilter(c, h.DB)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	if ownerF.Type == "" {
		BadRequest(c, "owner query param is required (user:<id> or org:<slug>)")
		return
	}
	if userID > 0 && h.Authz != nil {
		if err := h.Authz.EnsureOwnerReadable(c.Request.Context(), userID, service.OwnerRef{Type: ownerF.Type, ID: ownerF.ID}); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	from, to, err := parseUsageRange(c)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	buckets, err := h.Svc.OwnerSeries(c.Request.Context(), ownerF.Type, ownerF.ID, from, to)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"buckets": buckets})
}
