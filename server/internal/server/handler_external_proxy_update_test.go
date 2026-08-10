package server

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	_ "modernc.org/sqlite"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// newProviderUpdateTestServer wires the minimal Server slice the provider
// handlers need (externalProviderSvc over an in-memory SQLite) plus a gin
// router with an auth stub.
func newProviderUpdateTestServer(t *testing.T) (*gin.Engine, *service.ExternalProviderService, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE external_providers (
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
		created_by  TEXT NOT NULL DEFAULT 'user',
		created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	svc := service.NewExternalProviderService(store.New(store.Wrap(db)), db)
	srv := &Server{externalProviderSvc: svc}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("auth_user_id", int64(1)) })
	r.PUT("/api/me/external-proxy/providers/:id", srv.proxyUpdateProvider)
	return r, svc, db
}

// TestProxyUpdateProviderPatchSemantics locks the regression where a partial
// PUT (the create dialog's follow-up that only sets auth wiring) wiped the
// omitted label/api_base_url to "": absent fields must keep the stored value,
// explicitly sent fields (including "") must replace it.
func TestProxyUpdateProviderPatchSemantics(t *testing.T) {
	r, svc, _ := newProviderUpdateTestServer(t)
	ctx := t.Context()

	prov, err := svc.Create(ctx, "gh", "GitHub", "https://api.github.com")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	put := func(t *testing.T, body map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPut, "/api/me/external-proxy/providers/1", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// Partial PUT without label/api_base_url (the first-save follow-up).
	if w := put(t, map[string]any{
		"auth_type": "bearer", "auth_header": "Authorization", "auth_prefix": "Bearer",
		"whitelist": `[{"method":"GET","path":"*"}]`, "enabled": true,
	}); w.Code != http.StatusOK {
		t.Fatalf("partial PUT: status %d body %s", w.Code, w.Body)
	}
	got, err := svc.GetByID(ctx, prov.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Label != "GitHub" || got.APIBaseURL != "https://api.github.com" {
		t.Fatalf("absent fields must be preserved, got label=%q url=%q", got.Label, got.APIBaseURL)
	}

	// Explicit values (including clearing the label with "") replace.
	if w := put(t, map[string]any{"label": "", "api_base_url": "https://ghe.local/api"}); w.Code != http.StatusOK {
		t.Fatalf("explicit PUT: status %d body %s", w.Code, w.Body)
	}
	got, _ = svc.GetByID(ctx, prov.ID)
	if got.Label != "" || got.APIBaseURL != "https://ghe.local/api" {
		t.Fatalf("explicit fields must replace, got label=%q url=%q", got.Label, got.APIBaseURL)
	}

	// api_base_url cannot be cleared — it is required for the proxy to dial.
	if w := put(t, map[string]any{"api_base_url": ""}); w.Code != http.StatusBadRequest {
		t.Fatalf("clearing api_base_url should 400, got %d", w.Code)
	}
}

// TestProxyUpdateSystemProviderReadonly verifies the built-in-provider gate
// under PATCH semantics: a differing field 403s, a no-op full body or a pure
// enabled toggle passes.
func TestProxyUpdateSystemProviderReadonly(t *testing.T) {
	r, svc, db := newProviderUpdateTestServer(t)
	ctx := t.Context()

	if _, err := svc.Create(ctx, "builtin", "Built-in", "https://api.builtin.dev"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Flip to system-seeded (Create always stamps created_by='user').
	rawPut := func(t *testing.T, body map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPut, "/api/me/external-proxy/providers/1", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if _, err := db.Exec(`UPDATE external_providers SET created_by = 'system' WHERE id = 1`); err != nil {
		t.Fatalf("mark system: %v", err)
	}

	// Pure toggle: allowed.
	if w := rawPut(t, map[string]any{"enabled": false}); w.Code != http.StatusOK {
		t.Fatalf("enabled toggle should pass, got %d body %s", w.Code, w.Body)
	}
	// No-op full body (echoes stored values): allowed.
	if w := rawPut(t, map[string]any{
		"label": "Built-in", "api_base_url": "https://api.builtin.dev", "enabled": true,
	}); w.Code != http.StatusOK {
		t.Fatalf("no-op full body should pass, got %d body %s", w.Code, w.Body)
	}
	// A differing protected field: 403.
	if w := rawPut(t, map[string]any{"api_base_url": "https://evil.example"}); w.Code != http.StatusForbidden {
		t.Fatalf("mutating a system provider should 403, got %d", w.Code)
	}
}
