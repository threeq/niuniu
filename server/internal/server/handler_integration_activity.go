// handler_integration_activity.go — read side of the integration audit tables.
//
// Routes served (registered in router.go):
//
//	GET /api/me/integration-activity            merged feed, optional ?kind
//	GET /api/me/external-proxy/audit            merged feed pinned to kind=api
//	GET /api/me/data-sources/audit              merged feed pinned to kind=data
//
// The two kind-pinned routes exist so each family of activity is reachable
// under the route prefix its data already lives under; they are thin wrappers
// over the same handler as the merged route, not separate query paths.
package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// integrationActivity handles GET /api/me/integration-activity.
func (s *Server) integrationActivity(c *gin.Context) {
	s.writeActivityPage(c, "")
}

// proxyAudit handles GET /api/me/external-proxy/audit (external API calls only).
func (s *Server) proxyAudit(c *gin.Context) {
	s.writeActivityPage(c, service.ActivityKindAPI)
}

// dataSourceAudit handles GET /api/me/data-sources/audit (data queries only).
func (s *Server) dataSourceAudit(c *gin.Context) {
	s.writeActivityPage(c, service.ActivityKindData)
}

// writeActivityPage parses the filter query string, asks the service for one
// page, and writes it in the standard {data: ...} envelope. forcedKind, when
// non-empty, pins the kind regardless of the query string (used by the two
// prefix-scoped routes).
func (s *Server) writeActivityPage(c *gin.Context, forcedKind string) {
	userID := c.GetInt64("auth_user_id")
	if userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "authentication required"}})
		return
	}

	badRequest := func(msg string) {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": msg}})
	}

	in := service.IntegrationActivityInput{UserID: userID, Kind: forcedKind}
	if forcedKind == "" {
		kind := c.Query("kind")
		if kind != "" && kind != service.ActivityKindAPI && kind != service.ActivityKindData &&
			kind != service.ActivityKindFunction {
			badRequest("kind must be api, data, or function")
			return
		}
		in.Kind = kind
	}

	for _, f := range []struct {
		name string
		dst  *int64
	}{
		{"provider_id", &in.ProviderID},
		{"source_id", &in.SourceID},
		{"limit", &in.Limit},
		{"offset", &in.Offset},
	} {
		raw := c.Query(f.name)
		if raw == "" {
			continue
		}
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < 0 {
			badRequest(f.name + " must be a non-negative integer")
			return
		}
		*f.dst = v
	}

	var err error
	if in.From, err = parseActivityTime(c.Query("from"), false); err != nil {
		badRequest("from must be RFC3339 or YYYY-MM-DD")
		return
	}
	if in.To, err = parseActivityTime(c.Query("to"), true); err != nil {
		badRequest("to must be RFC3339 or YYYY-MM-DD")
		return
	}

	page, err := s.integrationAuditSvc.ListActivity(c.Request.Context(), in)
	if err != nil {
		if err == service.ErrBadActivityKind {
			badRequest("kind must be api, data, or function")
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL_ERROR", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": page})
}

// parseActivityTime accepts an RFC3339 timestamp, or a bare YYYY-MM-DD date
// (interpreted as UTC). A date-only upper bound is pushed to the exclusive end
// of that day so "to=2026-10-11" includes everything that happened on the 11th
// rather than only midnight. An empty string means "no bound".
func parseActivityTime(raw string, isUpperBound bool) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	t, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return time.Time{}, err
	}
	if isUpperBound {
		t = t.AddDate(0, 0, 1).Add(-time.Nanosecond)
	}
	return t, nil
}
