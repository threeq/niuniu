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
	"time"

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
		Operation string         `json:"operation"`
		Method    string         `json:"method"`
		Path      string         `json:"path"`
		Query     map[string]any `json:"query"`
		Body      map[string]any `json:"body"`
		SourceKey string         `json:"source_key"`
		DryRun    bool           `json:"dry_run"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": err.Error()}})
		return
	}

	// A call is addressed either by a contract operation name (semantic,
	// operation-level authorization — issue #737) or by an explicit
	// method+path pair (legacy). provider is always required.
	if req.Provider == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "provider is required"}})
		return
	}
	if req.Operation == "" && (req.Method == "" || req.Path == "") {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "either operation, or both method and path, are required"}})
		return
	}
	if req.Method != "" {
		switch strings.ToUpper(req.Method) {
		case "GET", "POST", "PUT", "PATCH", "DELETE":
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "unsupported HTTP method: " + req.Method}})
			return
		}
	}

	in := service.ProxyInput{
		UserID:      userID,
		Provider:    req.Provider,
		Operation:   req.Operation,
		Method:      req.Method,
		Path:        req.Path,
		Query:       req.Query,
		Body:        req.Body,
		WorkspaceID: c.GetInt64("mcp_workspace_id"), // 0 on SPA path -> service falls back to caller cred
		SourceKey:   req.SourceKey,
	}

	// dry_run: validate locally (visibility, whitelist, credential, contract
	// params) and return the verdict WITHOUT contacting the upstream.
	if req.DryRun {
		precheck, perr := s.externalProxySvc.PrecheckCall(c.Request.Context(), in)
		if perr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL_ERROR", "message": perr.Error()}})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": precheck})
		return
	}

	output, err := s.externalProxySvc.Call(c.Request.Context(), in)
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
			// outcome_unknown is an ambiguous transport failure on a write, not
			// an authz refusal — map it to 502 so the agent can tell "the call
			// may have applied upstream; do not blindly retry" apart from a 403.
			status := http.StatusForbidden
			if pe.Code == "outcome_unknown" {
				status = http.StatusBadGateway
			}
			c.JSON(status, gin.H{
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

// confirmProbe routes a non-GET verification probe through the chat permission
// card (in-workspace agents only), with the probe manifest as the card input so
// a human sees exactly which request will be sent. Returns (allowed, true) when
// the card ran; (false, false) when there is no workspace context to card
// against (the SPA confirmation surface is the caller's own UI).
func (s *Server) confirmProbe(c *gin.Context, workspaceID int64, provider string, probe *service.ProbeManifest, detail string) (bool, bool) {
	if workspaceID <= 0 || s.permService == nil || probe == nil {
		return false, false
	}
	ws, err := s.queries.GetWorkspace(c.Request.Context(), workspaceID)
	if err != nil {
		return false, false
	}
	resp, err := s.permService.Request(c.Request.Context(), ws.ID, ws.OwnerType, ws.OwnerID,
		ws.SessionID.String, "ProviderProbe", map[string]any{
			"provider": provider,
			"probe":    probe,
			"detail":   detail,
		})
	if err != nil {
		return false, false
	}
	return resp.Behavior == "allow", true
}

// proxyVerifyProvider handles POST /api/me/external-proxy/providers/:provider/verify
// and the /mcp equivalent. It runs the provider's read-only verification probe
// (auth + connectivity) and returns the verdict + probe manifest. It never
// changes the provider's enabled state.
func (s *Server) proxyVerifyProvider(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	if userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "authentication required"}})
		return
	}
	provider := c.Param("provider")
	var req struct {
		SourceKey string `json:"source_key"`
		Confirm   bool   `json:"confirm"`
	}
	_ = c.ShouldBindJSON(&req) // body is optional

	verifyWsID := c.GetInt64("mcp_workspace_id")
	in := service.VerifyProviderInput{
		Provider:    provider,
		UserID:      userID,
		WorkspaceID: verifyWsID,
		SourceKey:   req.SourceKey,
		// See proxyPublishProvider: an in-workspace agent cannot self-confirm;
		// only the permission card can approve a non-GET probe.
		Confirmed: req.Confirm && verifyWsID <= 0,
	}
	res, err := s.externalProxySvc.VerifyProvider(c.Request.Context(), in)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "NOT_FOUND", "message": err.Error()}})
		return
	}
	if res.RequiresConfirmation {
		if allowed, handled := s.confirmProbe(c, in.WorkspaceID, res.Provider, res.Probe, res.Detail); handled {
			if !allowed {
				c.JSON(http.StatusForbidden, gin.H{"data": res, "error": gin.H{"code": "denied", "message": "the verification probe was not approved"}})
				return
			}
			in.Confirmed = true
			res, err = s.externalProxySvc.VerifyProvider(c.Request.Context(), in)
			if err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "NOT_FOUND", "message": err.Error()}})
				return
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"data": res})
}

// proxyPublishProvider handles POST /api/me/external-proxy/providers/:id/publish
// and the /mcp equivalent. It lints the candidate contract, runs the
// verification probe, and only on success persists + enables the provider.
// A probe that is not a coarse read-only method (GET/HEAD) is routed through
// the chat permission card — with the probe manifest as the card input — so a
// human sees exactly what will be called before it runs.
func (s *Server) proxyPublishProvider(c *gin.Context) {
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
	var req struct {
		Profile   *string `json:"profile"`
		Confirm   bool    `json:"confirm"`
		SourceKey string  `json:"source_key"`
	}
	_ = c.ShouldBindJSON(&req)

	wsID := c.GetInt64("mcp_workspace_id")
	in := service.PublishProviderInput{
		ProviderID:  id,
		UserID:      userID,
		WorkspaceID: wsID,
		SourceKey:   req.SourceKey,
		Profile:     req.Profile,
		// An in-workspace agent must NEVER be able to self-confirm a non-GET
		// probe: its confirmation only ever comes from the chat permission card
		// below. The body flag is honoured only on the SPA path (WorkspaceID==0),
		// where the requesting user's own UI is the confirmation surface.
		Confirmed: req.Confirm && wsID <= 0,
	}

	res, err := s.externalProxySvc.PublishProvider(c.Request.Context(), in)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "NOT_FOUND", "message": err.Error()}})
		return
	}

	// Non-coarse probe + an in-workspace agent: show the probe on the
	// confirmation card and only proceed if the human allows it.
	if res.RequiresConfirmation {
		if allowed, handled := s.confirmProbe(c, in.WorkspaceID, res.Provider, res.Probe, res.Detail); handled {
			if !allowed {
				c.JSON(http.StatusForbidden, gin.H{"data": res, "error": gin.H{"code": "denied", "message": "the verification probe was not approved"}})
				return
			}
			in.Confirmed = true
			res, err = s.externalProxySvc.PublishProvider(c.Request.Context(), in)
			if err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "NOT_FOUND", "message": err.Error()}})
				return
			}
		}
	}

	// A publish that did not go live is a conflict (the caller must act), not
	// a server error — 409 carries the next_action / alternatives payload.
	status := http.StatusOK
	if !res.Published {
		status = http.StatusConflict
	}
	if res.Published {
		// Onboarding time: generate the operable capability description from the
		// newly published contract, so it is ready before the agent's first call
		// (Epic #726 / #738).
		s.refreshProviderCapability(c, id)
	}
	c.JSON(status, gin.H{"data": res})
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
		// Continuous health + contract drift (#744).
		HealthStatus        string     `json:"health_status"`
		ConsecutiveFailures int64      `json:"consecutive_failures"`
		LastProbeAt         *time.Time `json:"last_probe_at,omitempty"`
		LastProbeError      string     `json:"last_probe_error,omitempty"`
		AutoDisabledAt      *time.Time `json:"auto_disabled_at,omitempty"`
		ContractDrift       bool       `json:"contract_drift"`
		LastDriftAt         *time.Time `json:"last_drift_at,omitempty"`
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

			HealthStatus:        p.HealthStatus,
			ConsecutiveFailures: p.ConsecutiveFailures,
			LastProbeAt:         nullTimePtr(p.LastProbeAt),
			LastProbeError:      p.LastProbeError,
			AutoDisabledAt:      nullTimePtr(p.AutoDisabledAt),
			ContractDrift:       p.ContractDrift,
			LastDriftAt:         nullTimePtr(p.LastDriftAt),
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
	// In-workspace (MCP) callers may only read a provider's schema when it is
	// bound to their project — the same visibility list_providers enforces. The
	// SPA management path (no workspace context) keeps the full catalog.
	if !s.providerVisibleToWorkspace(c, providerName) {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "NOT_FOUND", "message": "provider not found: " + providerName}})
		return
	}
	prov, err := s.externalProviderSvc.GetByName(c.Request.Context(), providerName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "NOT_FOUND", "message": "provider not found: " + providerName}})
		return
	}

	contract := prov.Contract()
	// 写/敏感操作单列 (issue #737): the read-only vs write split is what the
	// confirmation card and the per-operation write control consume, so it is
	// surfaced explicitly rather than left for the agent to derive.
	readOps, writeOps := contract.SplitOperations()

	// Operable capability description (Epic #726 / #738): the "how to drive this
	// provider" document, generated from the confirmed contract and refreshed
	// here if the contract's digest moved. Never authored by a caller. Nil when
	// the capability service is unwired (partial test servers).
	var capDoc *service.CapabilityDoc
	if s.capabilitySvc != nil {
		capDoc, _ = s.capabilitySvc.EnsureProvider(c.Request.Context(), prov)
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
			// `contract` is the structured integration contract (operations /
			// auth / verify_operation / capabilities + a content digest) the
			// agent drives the provider with. Derived from the profile column;
			// a legacy free-text / loose-JSON profile degrades to an
			// empty-operations contract (raw text preserved in
			// legacy_profile) rather than failing — old providers never break.
			// `profile` is kept above for backward compatibility.
			"contract": contract,
			// Operation-level authorization view: the semantic operations that
			// run without the write toggle, and the write/sensitive ones that
			// require it. Empty for providers without a structured contract.
			"read_operations":  readOps,
			"write_operations": writeOps,
			// The operable capability description (structured operations +
			// natural-language usage/examples/limits) an agent drives the
			// provider with, plus the exact block injected into a workspace's
			// instruction file. Derived from the confirmed contract; a caller
			// cannot author it.
			"capability":             capDoc,
			"capability_agent_block": service.RenderCapabilityAgentBlock([]*service.CapabilityDoc{capDoc}),
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
