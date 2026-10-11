package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	_ "modernc.org/sqlite"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// newOpenAPIDraftTestServer wires the minimal Server slice proxyOpenAPIDraft
// needs (externalProviderSvc over an in-memory SQLite) plus a gin router with an
// auth stub. The SSRF gate is opened so the handler may fetch an httptest
// loopback document.
func newOpenAPIDraftTestServer(t *testing.T) (*gin.Engine, *service.ExternalProviderService) {
	t.Helper()

	prev := service.KBAllowLocalSources()
	service.SetKBAllowLocalSources(true)
	t.Cleanup(func() { service.SetKBAllowLocalSources(prev) })

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
	)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	svc := service.NewExternalProviderService(store.New(store.Wrap(db)), db)
	srv := &Server{externalProviderSvc: svc}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("auth_user_id", int64(1)) })
	r.POST("/api/me/external-proxy/providers/:id/openapi-draft", srv.proxyOpenAPIDraft)
	return r, svc
}

func petstoreDoc(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "service", "testdata", "openapi", "petstore3.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

// TestProxyOpenAPIDraft_GeneratesFromProvider: the endpoint fetches the
// provider's openapi_url and returns both draft shapes as untrusted data.
func TestProxyOpenAPIDraft_GeneratesFromProvider(t *testing.T) {
	docSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(petstoreDoc(t))
	}))
	defer docSrv.Close()

	r, svc := newOpenAPIDraftTestServer(t)
	ctx := t.Context()
	if _, err := svc.Create(ctx, "petstore", "Petstore", "https://api.petstore.example/v1"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Update(ctx, 1, "Petstore", "https://api.petstore.example/v1", "bearer", "Authorization", "Bearer",
		`[{"method":"GET","path":"*"}]`, "", docSrv.URL, true); err != nil {
		t.Fatalf("Update: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/me/external-proxy/providers/1/openapi-draft", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}

	var env struct {
		Data struct {
			Provider map[string]any `json:"provider"`
			Draft    map[string]any `json:"draft"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.Draft["untrusted_source"] != true {
		t.Fatalf("draft must be marked untrusted: %v", env.Data.Draft["untrusted_source"])
	}
	contract, ok := env.Data.Draft["provider_contract"].(map[string]any)
	if !ok || len(contract["operations"].([]any)) == 0 {
		t.Fatalf("provider contract draft missing operations: %v", env.Data.Draft["provider_contract"])
	}
	ds, ok := env.Data.Draft["http_data_source"].(map[string]any)
	if !ok {
		t.Fatalf("http data source draft missing: %v", env.Data.Draft)
	}
	scope := ds["scope_config"].(map[string]any)
	methods := scope["methods"].([]any)
	if len(methods) != 2 || methods[0] != "GET" || methods[1] != "HEAD" {
		t.Fatalf("scope must start read-only, got %v", methods)
	}
	if ds["default_access_mode"] != "read" {
		t.Fatalf("access mode must start read-only, got %v", ds["default_access_mode"])
	}
}

// TestProxyOpenAPIDraft_NoURL: a provider without an openapi_url is a 400, not a
// fetch attempt.
func TestProxyOpenAPIDraft_NoURL(t *testing.T) {
	r, svc := newOpenAPIDraftTestServer(t)
	if _, err := svc.Create(t.Context(), "plain", "Plain", "https://plain.dev"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/me/external-proxy/providers/1/openapi-draft", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a provider with no openapi_url, got %d body %s", w.Code, w.Body)
	}
}

// TestProxyOpenAPIDraft_BadDocument: a non-OpenAPI document yields 502 with a
// reason, never a panic.
func TestProxyOpenAPIDraft_BadDocument(t *testing.T) {
	docSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>not an api document</html>"))
	}))
	defer docSrv.Close()

	r, svc := newOpenAPIDraftTestServer(t)
	ctx := t.Context()
	if _, err := svc.Create(ctx, "bad", "Bad", "https://bad.dev"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Update(ctx, 1, "Bad", "https://bad.dev", "bearer", "Authorization", "Bearer",
		`[{"method":"GET","path":"*"}]`, "", docSrv.URL, true); err != nil {
		t.Fatalf("Update: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/me/external-proxy/providers/1/openapi-draft", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 for an unusable document, got %d body %s", w.Code, w.Body)
	}
}
