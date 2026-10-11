package server

import (
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

// newProviderSchemaTestServer wires the minimal Server slice proxyGetProviderSchema
// needs (externalProviderSvc over an in-memory SQLite) plus a gin router with
// an auth stub.
func newProviderSchemaTestServer(t *testing.T) (*gin.Engine, *service.ExternalProviderService) {
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
	r.GET("/api/me/external-proxy/providers/:provider/schema", srv.proxyGetProviderSchema)
	return r, svc
}

func getSchema(t *testing.T, r *gin.Engine, name string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/me/external-proxy/providers/"+name+"/schema", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET schema %s: status %d body %s", name, w.Code, w.Body)
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return env.Data
}

// TestProxyGetProviderSchema_StructuredContract: a provider whose profile is a
// structured contract returns the legacy fields intact plus a `contract` with
// operations and a digest.
func TestProxyGetProviderSchema_StructuredContract(t *testing.T) {
	r, svc := newProviderSchemaTestServer(t)
	ctx := t.Context()

	if _, err := svc.Create(ctx, "demo", "Demo", "https://api.demo.dev"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	profile := `{"schema_version":"1","provider":"Demo","auth":{"strategy":"bearer","header":"Authorization"},
	  "capabilities":["orders"],"verify_operation":"list_orders",
	  "operations":[{"name":"list_orders","method":"GET","path":"/orders","read_only":true,"summary":"list"}]}`
	if _, err := svc.Update(ctx, 1, "Demo", "https://api.demo.dev", "bearer", "Authorization", "Bearer",
		`[{"method":"GET","path":"*"}]`, profile, "", true); err != nil {
		t.Fatalf("Update: %v", err)
	}

	data := getSchema(t, r, "demo")

	// Backward-compatible fields are still present.
	if data["profile"] != profile {
		t.Fatalf("legacy profile field must be preserved, got %v", data["profile"])
	}
	if data["api_base_url"] != "https://api.demo.dev" {
		t.Fatalf("api_base_url missing: %v", data["api_base_url"])
	}

	contract, ok := data["contract"].(map[string]any)
	if !ok {
		t.Fatalf("contract missing or wrong type: %v", data["contract"])
	}
	if contract["digest"] == nil || contract["digest"] == "" {
		t.Fatalf("contract.digest must be set, got %v", contract["digest"])
	}
	ops, ok := contract["operations"].([]any)
	if !ok || len(ops) == 0 {
		t.Fatalf("contract.operations must be a non-empty array, got %v", contract["operations"])
	}
	first, _ := ops[0].(map[string]any)
	if first["name"] != "list_orders" || first["method"] != "GET" {
		t.Fatalf("unexpected first operation: %v", first)
	}
	if contract["verify_operation"] != "list_orders" {
		t.Fatalf("verify_operation not surfaced: %v", contract["verify_operation"])
	}
}

// TestProxyGetProviderSchema_LegacyProviderNoCrash: an old provider with a
// free-text profile still returns 200, with an empty-operations contract that
// preserves the raw text — old providers never break.
func TestProxyGetProviderSchema_LegacyProviderNoCrash(t *testing.T) {
	r, svc := newProviderSchemaTestServer(t)
	ctx := t.Context()

	const legacy = "Free text describing the API. Not JSON at all."
	if _, err := svc.Create(ctx, "old", "Old", "https://old.dev"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Update(ctx, 1, "Old", "https://old.dev", "bearer", "Authorization", "Bearer",
		`[{"method":"GET","path":"*"}]`, legacy, "", true); err != nil {
		t.Fatalf("Update: %v", err)
	}

	data := getSchema(t, r, "old")
	contract, ok := data["contract"].(map[string]any)
	if !ok {
		t.Fatalf("contract missing: %v", data["contract"])
	}
	if contract["legacy_profile"] != legacy {
		t.Fatalf("legacy text must be preserved in contract.legacy_profile, got %v", contract["legacy_profile"])
	}
	if ops, ok := contract["operations"].([]any); !ok || len(ops) != 0 {
		t.Fatalf("legacy provider must yield an empty operations array (no crash), got %v", contract["operations"])
	}
	if contract["digest"] == nil || contract["digest"] == "" {
		t.Fatalf("digest must still be computed for a legacy provider, got %v", contract["digest"])
	}
}
