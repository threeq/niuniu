package server

// Authorization tests for PUT /api/me/external-proxy/providers/:id: provider rows
// are global (not owner-scoped), so mutating one is an administrative action and
// must be gated the same way the integration-health provider mutations are
// (requireProviderAdmin -> auth.RequireAdmin). A non-admin must get 403 and no
// mutation must be persisted; an admin must still succeed.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	_ "modernc.org/sqlite"

	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// newProviderUpdateAuthzServer wires proxyUpdateProvider with auth ENABLED and a
// caller carrying the given role, so the admin gate decides the outcome.
func newProviderUpdateAuthzServer(t *testing.T, role string) (*gin.Engine, *service.ExternalProviderService) {
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
	srv := &Server{
		externalProviderSvc: svc,
		cfg:                 &config.Config{Auth: config.AuthConfig{Enabled: true}},
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("auth_user_id", int64(2))
		if role != "" {
			c.Set("auth_role", role)
		}
	})
	r.PUT("/api/me/external-proxy/providers/:provider", srv.proxyUpdateProvider)
	return r, svc
}

func putProviderBody(t *testing.T, r *gin.Engine, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/api/me/external-proxy/providers/1", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestProxyUpdateProviderRequiresAdmin(t *testing.T) {
	ctx := t.Context()

	// A non-admin (role=member) is refused with 403 and nothing is persisted.
	rMember, svcMember := newProviderUpdateAuthzServer(t, "member")
	if _, err := svcMember.Create(ctx, "gh", "GitHub", "https://api.github.com"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if w := putProviderBody(t, rMember, map[string]any{"api_base_url": "https://evil.example"}); w.Code != http.StatusForbidden {
		t.Fatalf("a non-admin must get 403 on PUT /providers/:id, got %d body %s", w.Code, w.Body)
	}
	got, err := svcMember.GetByID(ctx, 1)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.APIBaseURL != "https://api.github.com" {
		t.Fatalf("a rejected update must not persist, got api_base_url=%q", got.APIBaseURL)
	}

	// An admin still succeeds.
	rAdmin, svcAdmin := newProviderUpdateAuthzServer(t, "admin")
	if _, err := svcAdmin.Create(ctx, "gh", "GitHub", "https://api.github.com"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if w := putProviderBody(t, rAdmin, map[string]any{"api_base_url": "https://api.github.com/v2"}); w.Code != http.StatusOK {
		t.Fatalf("an admin must succeed, got %d body %s", w.Code, w.Body)
	}
	got, err = svcAdmin.GetByID(ctx, 1)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.APIBaseURL != "https://api.github.com/v2" {
		t.Fatalf("the admin update must persist, got api_base_url=%q", got.APIBaseURL)
	}
}
