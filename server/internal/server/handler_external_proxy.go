// handler_external_proxy.go — HTTP handlers for the AI-Adaptive External
// API Proxy. These replace the old L4 work-item REST endpoints with a
// generic, schema-driven proxy layer.
//
// Routes served (registered in router.go):
//
//	POST /mcp/external-proxy/call
//	GET  /mcp/external-proxy/providers
//	GET  /mcp/external-proxy/providers/:provider/schema
//	POST /api/me/external-proxy/call
//	GET  /api/me/external-proxy/providers
//	GET  /api/me/external-proxy/providers/:provider/schema
package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// proxyCall handles POST /api/me/external-proxy/call and /mcp/external-proxy/call
func (s *Server) proxyCall(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	if userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "authentication required"}})
		return
	}

	var req struct {
		Provider  string         `json:"provider"`
		Method    string         `json:"method"`
		Path      string         `json:"path"`
		Query     map[string]any `json:"query"`
		Body      map[string]any `json:"body"`
		SourceKey string         `json:"source_key"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": err.Error()}})
		return
	}

	if req.Provider == "" || req.Method == "" || req.Path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "provider, method, and path are required"}})
		return
	}
	switch req.Method {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "unsupported HTTP method: " + req.Method}})
		return
	}

	output, err := s.externalProxySvc.Call(c.Request.Context(), service.ProxyInput{
		UserID:      userID,
		Provider:    req.Provider,
		Method:      req.Method,
		Path:        req.Path,
		Query:       req.Query,
		Body:        req.Body,
		WorkspaceID: c.GetInt64("mcp_workspace_id"), // 0 on SPA path -> service falls back to caller cred
		SourceKey:   req.SourceKey,
	})
	if err != nil {
		// Credential-resolution failures are client-side faults, not upstream
		// outages — map them to 4xx so the agent doesn't misread a 502 as a
		// transient upstream error and retry.
		switch {
		case errors.Is(err, service.ErrForbidden):
			c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"code": "FORBIDDEN", "message": "no access to this project's external source"}})
			return
		case errors.Is(err, service.ErrAmbiguousSource):
			// Surface the available source_keys so the agent can retry with a
			// valid one. The keys are also embedded in err.Error() (which the
			// MCP shim relays verbatim), so the recovery hint reaches the agent
			// even though it reads the message string rather than this field.
			body := gin.H{"code": "AMBIGUOUS_SOURCE", "message": err.Error()}
			var ambErr *service.AmbiguousSourceError
			if errors.As(err, &ambErr) {
				body["provider"] = ambErr.Provider
				body["available_source_keys"] = ambErr.AvailableKeys
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": body})
			return
		case errors.Is(err, service.ErrNoProjectSource), errors.Is(err, service.ErrSourceNoCredential):
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "NO_SOURCE", "message": err.Error()}})
			return
		}
		if pe, ok := err.(*service.ProxyError); ok {
			c.JSON(http.StatusForbidden, gin.H{
				"error": gin.H{
					"code":     pe.Code,
					"message":  pe.Message,
					"provider": pe.Provider,
				},
			})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"code": "PROXY_ERROR", "message": err.Error()}})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": output})
}

// proxyListProviders handles GET /api/me/external-proxy/providers and /mcp/external-proxy/providers
func (s *Server) proxyListProviders(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	if userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "authentication required"}})
		return
	}

	providers, err := s.externalProviderSvc.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL_ERROR", "message": err.Error()}})
		return
	}

	// MCP path (agent running inside a workspace): scope the provider list to
	// only the providers actually bound to THIS project as external sources
	// (workspace -> issue -> project (1:1) -> project_external_sources). A
	// project with no sources gets an empty list -- the agent should not see
	// the user's whole integration catalog. The SPA settings page reaches this
	// same handler via /api/me/... where mcp_workspace_id is absent, so it
	// keeps the full list for management.
	if wsID := c.GetInt64("mcp_workspace_id"); wsID > 0 {
		allowed := s.workspaceSourceProviderNames(c.Request.Context(), wsID)
		scoped := make([]service.ProviderDef, 0, len(providers))
		for _, p := range providers {
			if allowed[p.Name] {
				scoped = append(scoped, p)
			}
		}
		providers = scoped
	}

	// Load per-user write prefs once and merge into each row. Absent means
	// disabled (default-off security gate enforced by the proxy at call time).
	writePrefs, err := s.externalProviderSvc.ListWritePrefs(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL_ERROR", "message": err.Error()}})
		return
	}

	type providerItem struct {
		ID           int64    `json:"id"`
		Name         string   `json:"name"`
		Label        string   `json:"label"`
		APIBaseURL   string   `json:"api_base_url"`
		Enabled      bool     `json:"enabled"`
		WriteEnabled bool     `json:"write_enabled"`
		AuthType     string   `json:"auth_type"`
		AuthModes    []string `json:"auth_modes"`
		OpenAPIURL   string   `json:"openapi_url,omitempty"`
		CreatedBy    string   `json:"created_by"`
	}
	items := make([]providerItem, 0, len(providers))
	for _, p := range providers {
		items = append(items, providerItem{
			ID:           p.ID,
			Name:         p.Name,
			Label:        p.Label,
			APIBaseURL:   p.APIBaseURL,
			Enabled:      p.Enabled,
			WriteEnabled: writePrefs[p.ID],
			AuthType:     p.AuthType,
			AuthModes:    p.AuthModes(),
			OpenAPIURL:   p.OpenAPIURL,
			CreatedBy:    p.CreatedBy,
		})
	}

	c.JSON(http.StatusOK, gin.H{"data": gin.H{"items": items}})
}

// workspaceSourceProviderNames returns the set of provider names bound as
// external sources to the workspace's project (workspace -> issue -> project).
// On any resolution error -- e.g. a temporary/studio workspace with no issue,
// or a project with no sources -- it returns an empty set, which scopes the MCP
// provider list to nothing rather than leaking the user's full catalog.
func (s *Server) workspaceSourceProviderNames(ctx context.Context, workspaceID int64) map[string]bool {
	allowed := map[string]bool{}
	projectID, err := s.queries.GetProjectIDForWorkspace(ctx, workspaceID)
	if err != nil {
		slog.Warn("list_providers scope: resolve project for workspace failed",
			"workspace_id", workspaceID, "error", err)
		return allowed
	}
	sources, err := s.extSourceSvc.List(ctx, projectID)
	if err != nil {
		slog.Warn("list_providers scope: list external sources failed",
			"project_id", projectID, "error", err)
		return allowed
	}
	for _, src := range sources {
		allowed[string(src.Provider)] = true
	}
	return allowed
}

// proxySetWriteEnabled handles PATCH /api/me/external-proxy/providers/:id/write-enabled
// with body {enabled: bool}. Flips the user's external_api_write_prefs row,
// which the proxy consults to gate non-GET calls.
func (s *Server) proxySetWriteEnabled(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	if userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "authentication required"}})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "invalid provider id"}})
		return
	}
	// Resolve the provider so we 404 cleanly on bad ids (otherwise the
	// upsert would silently insert a row pointing nowhere — UNIQUE +
	// FK constraints on external_api_write_prefs would actually catch
	// this, but a clear 404 reads better in the SPA).
	if _, err := s.externalProviderSvc.GetByID(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "NOT_FOUND", "message": "provider not found"}})
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": err.Error()}})
		return
	}
	if err := s.externalProviderSvc.SetWritePref(c.Request.Context(), userID, id, req.Enabled); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL_ERROR", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"ok": true, "enabled": req.Enabled}})
}

// proxyGetProviderSchema handles GET /api/me/external-proxy/providers/:provider/schema
// and /mcp/external-proxy/providers/:provider/schema
func (s *Server) proxyGetProviderSchema(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	if userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "authentication required"}})
		return
	}

	providerName := c.Param("provider")
	prov, err := s.externalProviderSvc.GetByName(c.Request.Context(), providerName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "NOT_FOUND", "message": "provider not found: " + providerName}})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data": gin.H{
			"id":           prov.ID,
			"name":         prov.Name,
			"label":        prov.Label,
			"api_base_url": prov.APIBaseURL,
			"auth_type":    prov.AuthType,
			"auth_modes":   prov.AuthModes(),
			"auth_header":  prov.AuthHeader,
			"auth_prefix":  prov.AuthPrefix,
			"profile":      prov.Profile,
			"openapi_url":  prov.OpenAPIURL,
			// `whitelist` is the curated "default safe set" — consulted by
			// the proxy only when the per-user `write_enabled` toggle is
			// OFF. Kept in the schema response so the SPA Edit dialog can
			// round-trip a user-defined provider's tuned set without
			// silently overwriting it with the default on save.
			"whitelist":  prov.Whitelist,
			"enabled":    prov.Enabled,
			"created_by": prov.CreatedBy,
		},
	})
}

// proxyCreateProvider handles POST /api/me/external-proxy/providers
func (s *Server) proxyCreateProvider(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	if userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "authentication required"}})
		return
	}

	var req struct {
		Name       string `json:"name"`
		Label      string `json:"label"`
		APIBaseURL string `json:"api_base_url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": err.Error()}})
		return
	}
	if req.Name == "" || req.APIBaseURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "name and api_base_url are required"}})
		return
	}

	prov, err := s.externalProviderSvc.Create(c.Request.Context(), req.Name, req.Label, req.APIBaseURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL_ERROR", "message": err.Error()}})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"data": prov})
}

// proxyUpdateProvider handles PUT /api/me/external-proxy/providers/:id
func (s *Server) proxyUpdateProvider(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	if userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "authentication required"}})
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "invalid provider id"}})
		return
	}

	// Pointer fields give the endpoint real PATCH semantics: an ABSENT field
	// keeps the stored value, an explicit value (including "") replaces it.
	// The plain-string version overwrote every omitted field with "" — a
	// partial PUT (e.g. the create dialog's follow-up that only sets auth
	// wiring) silently wiped label and api_base_url.
	var req struct {
		Label      *string `json:"label"`
		APIBaseURL *string `json:"api_base_url"`
		AuthType   *string `json:"auth_type"`
		AuthHeader *string `json:"auth_header"`
		AuthPrefix *string `json:"auth_prefix"`
		Whitelist  *string `json:"whitelist"`
		Profile    *string `json:"profile"`
		OpenAPIURL *string `json:"openapi_url"`
		Enabled    *bool   `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": err.Error()}})
		return
	}

	// Fetch existing provider to preserve fields not in the PATCH body.
	existing, err := s.externalProviderSvc.GetByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "NOT_FOUND", "message": "provider not found"}})
		return
	}

	pick := func(p *string, cur string) string {
		if p != nil {
			return *p
		}
		return cur
	}
	changes := func(p *string, cur string) bool { return p != nil && *p != cur }

	// System-seeded providers are immutable except for the enabled toggle.
	// The user can disable a built-in provider, but cannot rename it, change
	// its API base URL, auth wiring, or whitelist — these are part of the
	// product surface. Only a field that actually differs counts as a
	// mutation, so a full-body no-op PUT doesn't 403.
	if existing.CreatedBy == service.CreatedBySystem {
		anyMutation := changes(req.Label, existing.Label) ||
			changes(req.APIBaseURL, existing.APIBaseURL) ||
			changes(req.AuthType, existing.AuthType) ||
			changes(req.AuthHeader, existing.AuthHeader) ||
			changes(req.AuthPrefix, existing.AuthPrefix) ||
			changes(req.Whitelist, existing.Whitelist) ||
			changes(req.Profile, existing.Profile) ||
			changes(req.OpenAPIURL, existing.OpenAPIURL)
		if anyMutation {
			c.JSON(http.StatusForbidden, gin.H{"error": gin.H{
				"code":    "SYSTEM_PROVIDER_READONLY",
				"message": "built-in providers can only be enabled/disabled; other fields are read-only",
			}})
			return
		}
	}

	enabled := existing.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	apiBaseURL := pick(req.APIBaseURL, existing.APIBaseURL)
	if strings.TrimSpace(apiBaseURL) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "api_base_url cannot be empty"}})
		return
	}

	prov, err := s.externalProviderSvc.Update(c.Request.Context(), id,
		pick(req.Label, existing.Label), apiBaseURL,
		pick(req.AuthType, existing.AuthType),
		pick(req.AuthHeader, existing.AuthHeader),
		pick(req.AuthPrefix, existing.AuthPrefix),
		pick(req.Whitelist, existing.Whitelist),
		pick(req.Profile, existing.Profile),
		pick(req.OpenAPIURL, existing.OpenAPIURL),
		enabled)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL_ERROR", "message": err.Error()}})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": prov})
}

// proxyDeleteProvider handles DELETE /api/me/external-proxy/providers/:id
func (s *Server) proxyDeleteProvider(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "invalid provider id"}})
		return
	}
	existing, err := s.externalProviderSvc.GetByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "NOT_FOUND", "message": "provider not found"}})
		return
	}
	if existing.CreatedBy == service.CreatedBySystem {
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{
			"code":    "SYSTEM_PROVIDER_READONLY",
			"message": "built-in providers cannot be deleted",
		}})
		return
	}
	if err := s.externalProviderSvc.Delete(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL_ERROR", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"ok": true}})
}
