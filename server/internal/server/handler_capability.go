// handler_capability.go — the read surface for operable capability descriptions
// (Epic #726 / #738).
//
// Serves both discovery lines through one route:
//
//	GET /mcp/capabilities/:subject_kind/:ref   (describe_source MCP tool)
//	GET /api/me/capabilities/:subject_kind/:ref (SPA "view what the agent sees")
//
// :subject_kind is `provider` or `data_source`; :ref is a provider name, or a
// data source id/name. The response carries the structured doc plus the EXACT
// block injected into the workspace instruction file (agent_block), so the user
// can see, byte for byte, what the agent reads.
//
// Read-only by construction: the handler only ever calls the CapabilityService's
// Ensure* methods, which rebuild the doc from the confirmed contract/metadata.
// There is no request field that accepts capability text, so no caller — human
// or agent — can author or edit a description through this (or any) endpoint.
package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// getCapability handles the capability read for both the MCP and SPA paths.
func (s *Server) getCapability(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	if userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "authentication required"}})
		return
	}
	kind := strings.ToLower(strings.TrimSpace(c.Param("subject_kind")))
	ref := strings.TrimSpace(c.Param("ref"))
	if ref == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "a provider name or data source id/name is required"}})
		return
	}
	if s.capabilitySvc == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "UNAVAILABLE", "message": "capability descriptions are not available"}})
		return
	}

	ctx := c.Request.Context()
	var (
		doc *service.CapabilityDoc
		err error
	)
	switch kind {
	case service.CapabilityTargetProvider:
		// In-workspace (MCP) callers are scoped to the providers actually bound
		// to their project's external sources — the same policy list_providers
		// applies — so an agent cannot describe a provider it was not given.
		if !s.providerVisibleToWorkspace(c, ref) {
			// Not-found rather than 403 so an in-workspace agent cannot probe
			// which providers exist outside its binding.
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "NOT_FOUND", "message": "provider not found: " + ref}})
			return
		}
		doc, err = s.capabilitySvc.EnsureProviderByName(ctx, ref)
	case service.CapabilityTargetDataSource:
		var id int64
		id, err = s.resolveCapabilityDataSourceID(c, ref, userID)
		if err == nil {
			doc, err = s.capabilitySvc.EnsureDataSourceByID(ctx, id, userID, s.capabilityCallerOrgIDs(c, userID))
		}
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "subject_kind must be 'provider' or 'data_source'"}})
		return
	}

	if err != nil {
		if errors.Is(err, service.ErrDataSourceNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "NOT_FOUND", "message": "not found: " + ref}})
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "NOT_FOUND", "message": err.Error()}})
		return
	}
	if doc == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "NOT_FOUND", "message": "no capability description for " + ref}})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"doc": doc,
		// The exact block injected into the workspace instruction file, so the
		// user can audit what the agent actually reads.
		"agent_block": service.RenderCapabilityAgentBlock([]*service.CapabilityDoc{doc}),
	}})
}

// resolveCapabilityDataSourceID resolves a data source reference (id or name) to
// an id the caller may see. In-workspace (MCP) callers are additionally scoped
// to the sources bound to the workspace's project, exactly like run_data_query;
// SPA callers are scoped to their accessible owners.
func (s *Server) resolveCapabilityDataSourceID(c *gin.Context, ref string, userID int64) (int64, error) {
	if s.dataSourceSvc == nil {
		return 0, service.ErrDataSourceNotFound
	}
	if wsID := c.GetInt64("mcp_workspace_id"); wsID > 0 {
		ws, err := s.queries.GetWorkspace(c.Request.Context(), wsID)
		if err != nil {
			return 0, service.ErrDataSourceNotFound
		}
		return s.dataSourceSvc.ResolveSourceIDForWorkspace(c.Request.Context(), ref, ws, userID, wsOrgIDs(ws))
	}
	return s.dataSourceSvc.ResolveSourceID(c.Request.Context(), ref, userID, s.capabilityCallerOrgIDs(c, userID))
}

// providerVisibleToWorkspace reports whether an in-workspace (MCP) caller may
// act on a provider: only providers bound to the workspace's project as external
// sources are visible, exactly like list_providers. A caller with no workspace
// context (the SPA management surface) sees the full catalog. This is the single
// scoping gate shared by every provider-discovery read (get_provider_schema,
// describe_source), so an agent can never describe a provider it was not given.
func (s *Server) providerVisibleToWorkspace(c *gin.Context, name string) bool {
	wsID := c.GetInt64("mcp_workspace_id")
	if wsID <= 0 {
		return true
	}
	return s.workspaceSourceProviderNames(c.Request.Context(), wsID)[name]
}

// capabilityCallerOrgIDs mirrors DataSourceHandler.callerOrgIDs: the orgs the
// caller belongs to, for owner-visible lookups. Nil when Authz is unwired.
func (s *Server) capabilityCallerOrgIDs(c *gin.Context, userID int64) []int64 {
	if s.authzSvc == nil {
		return nil
	}
	owners, err := s.authzSvc.Accessible(c.Request.Context(), userID)
	if err != nil {
		return nil
	}
	return owners.OrgIDs
}

// refreshProviderCapability regenerates + caches the capability doc for a
// provider after it goes live. Best-effort: the read surface rebuilds from the
// contract anyway, so a failure here only means the cache is refreshed later.
func (s *Server) refreshProviderCapability(c *gin.Context, providerID int64) {
	if s.capabilitySvc == nil {
		return
	}
	p, err := s.externalProviderSvc.GetByID(c.Request.Context(), providerID)
	if err != nil {
		return
	}
	_, _ = s.capabilitySvc.EnsureProvider(c.Request.Context(), p)
}

// refreshDataSourceCapability regenerates + caches the capability doc for a data
// source after create/update. Best-effort, same as above.
func (s *Server) refreshDataSourceCapability(c *gin.Context, id, userID int64, orgIDs []int64) {
	if s.capabilitySvc == nil || id == 0 {
		return
	}
	_, _ = s.capabilitySvc.EnsureDataSourceByID(c.Request.Context(), id, userID, orgIDs)
}
