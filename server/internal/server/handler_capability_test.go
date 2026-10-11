package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	_ "modernc.org/sqlite"

	"github.com/niuniu-dev/niuniu/internal/agentproxy"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// newCapabilityTestServer wires the minimal Server slice getCapability needs:
// a provider service plus a capability service over an in-memory SQLite that has
// the integration_capabilities table. The returned Server can be re-mounted on a
// router with a different auth/workspace middleware for scoping tests.
func newCapabilityTestServerParts(t *testing.T) (*service.ExternalProviderService, *Server) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	stmts := []string{
		`CREATE TABLE external_providers (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			name        TEXT NOT NULL UNIQUE,
			label       TEXT NOT NULL DEFAULT '',
			api_base_url TEXT NOT NULL,
			auth_type   TEXT NOT NULL DEFAULT 'bearer',
			auth_header TEXT NOT NULL DEFAULT 'Authorization',
			auth_prefix TEXT NOT NULL DEFAULT 'Bearer',
			profile     TEXT NOT NULL DEFAULT '',
			openapi_url TEXT NOT NULL DEFAULT '',
			whitelist   TEXT NOT NULL DEFAULT '[]',
			enabled     INTEGER NOT NULL DEFAULT 1,
			health_status       TEXT NOT NULL DEFAULT 'unknown',
			consecutive_failures INTEGER NOT NULL DEFAULT 0,
			last_probe_at       TIMESTAMP DEFAULT NULL,
			last_probe_error    TEXT NOT NULL DEFAULT '',
			auto_disabled_at    TIMESTAMP DEFAULT NULL,
			probe_interval_seconds INTEGER NOT NULL DEFAULT 86400,
			upstream_spec_digest TEXT NOT NULL DEFAULT '',
			contract_drift      INTEGER NOT NULL DEFAULT 0,
			last_drift_at       TIMESTAMP DEFAULT NULL,
			created_by  TEXT NOT NULL DEFAULT 'user',
			created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE integration_capabilities (
			id INTEGER PRIMARY KEY AUTOINCREMENT, subject_kind TEXT NOT NULL,
			subject_id INTEGER NOT NULL, source_digest TEXT NOT NULL DEFAULT '',
			doc TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (subject_kind, subject_id))`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("ddl: %v", err)
		}
	}

	q := store.New(store.Wrap(db))
	provSvc := service.NewExternalProviderService(q, db)
	capSvc := service.NewCapabilityService(q, db, provSvc, nil, nil)
	srv := &Server{externalProviderSvc: provSvc, capabilitySvc: capSvc, queries: q}
	return provSvc, srv
}

// newCapabilityTestServer mounts the parts on a router with a plain auth
// middleware (no workspace context: the SPA path).
func newCapabilityTestServer(t *testing.T) (*gin.Engine, *service.ExternalProviderService) {
	t.Helper()
	provSvc, srv := newCapabilityTestServerParts(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("auth_user_id", int64(1)) })
	r.GET("/api/me/capabilities/:subject_kind/:ref", srv.getCapability)
	return r, provSvc
}

const capTestContract = `{"schema_version":"1","provider":"Demo","base_url":"https://api.demo.dev",
  "verify_operation":"list_orders",
  "operations":[{"name":"list_orders","method":"GET","path":"/orders","read_only":true,"summary":"list"}]}`

// TestGetCapability_ReturnsDocAndAgentBlock: the discovery endpoint returns the
// structured doc AND the exact DATA-fenced block injected into the workspace
// instruction file.
func TestGetCapability_ReturnsDocAndAgentBlock(t *testing.T) {
	r, provSvc := newCapabilityTestServer(t)
	ctx := t.Context()
	if _, err := provSvc.Create(ctx, "demo", "Demo", "https://api.demo.dev"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := provSvc.Update(ctx, 1, "Demo", "https://api.demo.dev", "bearer", "Authorization", "Bearer",
		`[{"method":"GET","path":"*"}]`, capTestContract, "", true); err != nil {
		t.Fatalf("Update: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/me/capabilities/provider/demo", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	var env struct {
		Data struct {
			Doc        map[string]any `json:"doc"`
			AgentBlock string         `json:"agent_block"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.Doc["target_kind"] != "provider" || env.Data.Doc["tool"] != "call_external_api" {
		t.Fatalf("unexpected doc: %v", env.Data.Doc)
	}
	if env.Data.Doc["usage"] == nil || env.Data.Doc["source_digest"] == "" {
		t.Fatalf("doc missing narrative/digest: %v", env.Data.Doc)
	}
	if !strings.Contains(env.Data.AgentBlock, agentproxy.CapabilityBlockBegin) ||
		!strings.Contains(env.Data.AgentBlock, "非指令") {
		t.Fatalf("agent_block not partitioned:\n%s", env.Data.AgentBlock)
	}
}

// TestGetCapability_UnknownKindRejected: an unknown subject_kind is a 400.
func TestGetCapability_UnknownKindRejected(t *testing.T) {
	r, _ := newCapabilityTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/me/capabilities/nonsense/demo", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", w.Code)
	}
}

// TestCapabilityRoutesAreReadOnly asserts the capability read surface is GET
// only: a caller cannot PUT/POST a description. Combined with the absence of any
// authoring parameter on the MCP tool, this is the structural proof that a
// description change must go through the (contract-driven) pipeline.
func TestCapabilityRoutesAreReadOnly(t *testing.T) {
	r, _ := newCapabilityTestServer(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		req := httptest.NewRequest(method, "/api/me/capabilities/provider/demo", strings.NewReader(`{"doc":"forged"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code == http.StatusOK {
			t.Fatalf("%s on the capability endpoint must not succeed (status %d)", method, w.Code)
		}
	}
}

// TestGetCapability_ProviderScopedForWorkspaceAgents is the IDOR regression: an
// in-workspace (MCP) caller may only describe providers bound to its project. A
// provider that exists but is not bound is reported as not-found (no existence
// probe), while the SPA path (no workspace) still sees the management catalog.
func TestGetCapability_ProviderScopedForWorkspaceAgents(t *testing.T) {
	provSvc, srv := newCapabilityTestServerParts(t)
	ctx := t.Context()
	if _, err := provSvc.Create(ctx, "demo", "Demo", "https://api.demo.dev"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := provSvc.Update(ctx, 1, "Demo", "https://api.demo.dev", "bearer", "Authorization", "Bearer",
		`[{"method":"GET","path":"*"}]`, capTestContract, "", true); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Mount the same handler with a workspace context. The test DB has no
	// project/issue linkage, so the bound-provider set is empty → not-found.
	gin.SetMode(gin.TestMode)
	w := gin.New()
	w.Use(func(c *gin.Context) {
		c.Set("auth_user_id", int64(1))
		c.Set("mcp_workspace_id", int64(1))
	})
	w.GET("/api/me/capabilities/:subject_kind/:ref", srv.getCapability)

	rec := httptest.NewRecorder()
	w.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/me/capabilities/provider/demo", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("in-workspace describe of an unbound provider: status %d, want 404 (body %s)", rec.Code, rec.Body)
	}

	// The SPA path (no workspace context) still resolves it.
	spa := gin.New()
	spa.Use(func(c *gin.Context) { c.Set("auth_user_id", int64(1)) })
	spa.GET("/api/me/capabilities/:subject_kind/:ref", srv.getCapability)
	rec2 := httptest.NewRecorder()
	spa.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/api/me/capabilities/provider/demo", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("SPA describe: status %d, want 200 (body %s)", rec2.Code, rec2.Body)
	}
}

// TestGetProviderSchema_ScopedForWorkspaceAgents: the same provider-scoping gate
// guards get_provider_schema, so the IDOR class is closed on both discovery
// reads (the schema endpoint now also returns the capability doc).
func TestGetProviderSchema_ScopedForWorkspaceAgents(t *testing.T) {
	provSvc, srv := newCapabilityTestServerParts(t)
	ctx := t.Context()
	if _, err := provSvc.Create(ctx, "demo", "Demo", "https://api.demo.dev"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	gin.SetMode(gin.TestMode)
	w := gin.New()
	w.Use(func(c *gin.Context) {
		c.Set("auth_user_id", int64(1))
		c.Set("mcp_workspace_id", int64(1))
	})
	w.GET("/api/me/external-proxy/providers/:provider/schema", srv.proxyGetProviderSchema)

	rec := httptest.NewRecorder()
	w.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/me/external-proxy/providers/demo/schema", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("in-workspace schema read of an unbound provider: status %d, want 404", rec.Code)
	}
}

// TestCapabilityRouteNoTreeConflict registers the capability route alongside the
// existing /capability-* routes on one engine. Gin panics at registration on a
// radix-tree conflict, so this guards against /capabilities/:subject_kind/:ref
// colliding with /capability-modules or /capability-backends at startup.
func TestCapabilityRouteNoTreeConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := func(c *gin.Context) { c.Status(http.StatusOK) }
	api := r.Group("/api")
	api.GET("/capability-modules", h)
	backends := api.Group("/capability-backends")
	backends.GET("", h)
	backends.POST("", h)
	backends.PUT("/:id", h)
	backends.DELETE("/:id", h)
	me := api.Group("/me")
	me.GET("/capabilities/:subject_kind/:ref", h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/me/capabilities/provider/demo", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("capability route did not resolve: status %d", w.Code)
	}
}
