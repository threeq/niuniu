// handler_integration_health.go -- continuous integration-source health
// (Epic #726 / #744).
//
// Routes (registered in router.go):
//
//	POST /api/me/integration-health/:kind/:id/probe
//	POST /api/me/integration-health/:kind/:id/reenable
//	POST /api/me/integration-health/:kind/:id/disable
//	POST /api/me/integration-health/provider/:id/acknowledge-drift
//	GET  /api/me/integration-health/runs
//
// :kind is "provider" or "data_source". Provider actions are global (providers
// are not owner-scoped); data-source actions verify the caller can reach the
// source through the same owner gate the rest of the data-source API uses.
package server

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/auth"
	"github.com/niuniu-dev/niuniu/internal/notify"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// nullTimePtr renders a nullable timestamp as a *time.Time so it serializes to
// RFC3339 or is omitted (matches the api package's helper of the same shape).
func nullTimePtr(nt sql.NullTime) *time.Time {
	if !nt.Valid {
		return nil
	}
	t := nt.Time
	return &t
}

// integrationHealthNotifier delivers health alerts to the station-in
// notification hub and, best-effort, to the owner's IM Bot chats. Implements
// service.IntegrationHealthNotifier.
type integrationHealthNotifier struct {
	hub      *notify.NotificationHub
	imbotSvc *service.IMBotService
}

// NotifyIntegrationHealth broadcasts a station-in notification (the SPA
// invalidates the integration lists and shows a localized toast) and pushes a
// short text to the owner's active IM chats when the alert has an owner.
func (n *integrationHealthNotifier) NotifyIntegrationHealth(ctx context.Context, alert service.IntegrationHealthAlert) {
	if n.hub != nil {
		n.hub.Broadcast(notify.Notification{
			Topic:  notify.TopicIntegrationHealth,
			Action: alert.Kind,
			ID:     alert.SubjectID,
			Extra: gin.H{
				"subject_kind": alert.SubjectKind,
				"name":         alert.Name,
				"message":      alert.Message,
			},
			OwnerType: alert.OwnerType,
			OwnerID:   alert.OwnerID,
		})
	}
	// IM Bot push is owner-scoped; a global provider has no owner, so it is
	// station-in only. Best-effort -- a push failure must not break the sweep.
	if n.imbotSvc != nil && alert.OwnerType != "" && alert.OwnerID != 0 {
		n.imbotSvc.NotifyOwnerText(ctx, service.OwnerRef{Type: alert.OwnerType, ID: alert.OwnerID},
			"[集成健康] "+alert.Name+"："+alert.Message)
	}
}

// probe runs (or re-runs) the health probe for one source immediately.
func (s *Server) integrationHealthProbe(c *gin.Context) {
	uid := c.GetInt64("auth_user_id")
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	kind, id, ok := s.integrationHealthParams(c)
	if !ok {
		return
	}
	if kind == service.ProbeSubjectDataSource && !s.callerCanReachDataSource(c, uid, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "data source not found"})
		return
	}
	if kind == service.ProbeSubjectProvider && !s.requireProviderAdmin(c) {
		return
	}
	var (
		outcome *service.ProbeOutcome
		err     error
	)
	switch kind {
	case service.ProbeSubjectProvider:
		outcome, err = s.integrationHealthSvc.ProbeProvider(c.Request.Context(), id)
	case service.ProbeSubjectDataSource:
		outcome, err = s.integrationHealthSvc.ProbeDataSource(c.Request.Context(), id)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown subject kind"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"outcome": outcome})
}

// reenable clears an auto-disable (or manual off) and the failure streak.
func (s *Server) integrationHealthReenable(c *gin.Context) {
	s.integrationHealthSetEnabled(c, true)
}

// disable turns a source off manually.
func (s *Server) integrationHealthDisable(c *gin.Context) {
	s.integrationHealthSetEnabled(c, false)
}

func (s *Server) integrationHealthSetEnabled(c *gin.Context, enabled bool) {
	uid := c.GetInt64("auth_user_id")
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	kind, id, ok := s.integrationHealthParams(c)
	if !ok {
		return
	}
	if kind == service.ProbeSubjectDataSource && !s.callerCanReachDataSource(c, uid, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "data source not found"})
		return
	}
	if kind == service.ProbeSubjectProvider && !s.requireProviderAdmin(c) {
		return
	}
	if err := s.integrationHealthSvc.SetEnabled(c.Request.Context(), kind, id, enabled); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"enabled": enabled})
}

// acknowledgeDrift re-baselines a provider's upstream contract digest.
func (s *Server) integrationHealthAckDrift(c *gin.Context) {
	uid := c.GetInt64("auth_user_id")
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	kind, id, ok := s.integrationHealthParams(c)
	if !ok {
		return
	}
	if kind != service.ProbeSubjectProvider {
		c.JSON(http.StatusBadRequest, gin.H{"error": "contract drift only applies to providers"})
		return
	}
	if !s.requireProviderAdmin(c) {
		return
	}
	if err := s.integrationHealthSvc.AcknowledgeDrift(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"drift": false})
}

// runs returns recent probe history, scoped to the caller's owner set.
func (s *Server) integrationHealthRuns(c *gin.Context) {
	uid := c.GetInt64("auth_user_id")
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	kind := c.Query("kind")
	if kind != service.ProbeSubjectProvider && kind != service.ProbeSubjectDataSource {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind must be provider or data_source"})
		return
	}
	var subjectID int64
	if v := c.Query("id"); v != "" {
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil || parsed < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
			return
		}
		subjectID = parsed
	}
	var limit int64
	if v := c.Query("limit"); v != "" {
		limit, _ = strconv.ParseInt(v, 10, 64)
	}
	runs, err := s.integrationHealthSvc.ListProbeRuns(
		c.Request.Context(), kind, subjectID, uid, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": runs})
}

// integrationHealthParams parses and validates the :kind/:id path pair.
func (s *Server) integrationHealthParams(c *gin.Context) (kind string, id int64, ok bool) {
	kind = c.Param("kind")
	if kind != service.ProbeSubjectProvider && kind != service.ProbeSubjectDataSource {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind must be provider or data_source"})
		return "", 0, false
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return "", 0, false
	}
	return kind, id, true
}

// callerCanReachDataSource applies the data-source owner gate (personal owner
// or a member org), reusing DataSourceService.Get.
func (s *Server) callerCanReachDataSource(c *gin.Context, uid, id int64) bool {
	_, err := s.dataSourceSvc.Get(c.Request.Context(), id, uid, s.callerOrgIDs(c, uid))
	return err == nil
}

// callerOrgIDs resolves the orgs the caller belongs to. Nil when Authz is
// unwired or the lookup fails — personal scope only, never a wider set.
func (s *Server) callerOrgIDs(c *gin.Context, uid int64) []int64 {
	if s.authzSvc == nil {
		return nil
	}
	owners, err := s.authzSvc.Accessible(c.Request.Context(), uid)
	if err != nil {
		return nil
	}
	return owners.OrgIDs
}

// requireProviderAdmin gates an action on a GLOBAL provider behind the admin
// role. Providers are not owner-scoped, so there is no per-owner gate to apply
// (unlike data sources); the project's idiom for administrative actions is
// auth.RequireAdmin, which is a pass-through when auth is disabled (personal
// edition, single user). Returns false and has already written the 403 when the
// caller is not permitted.
//
// This is deliberately stricter than the pre-existing PUT /providers/:id (which
// any authenticated user can call), so the health endpoints cannot widen access
// to global provider state.
func (s *Server) requireProviderAdmin(c *gin.Context) bool {
	auth.RequireAdmin(s.cfg.Auth.Enabled)(c)
	return !c.IsAborted()
}
