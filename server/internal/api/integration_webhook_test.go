package api_test

// Inbound webhook status contract: oversized payloads are 413, an exhausted
// per-binding budget is 429, forgeries are 401 and an unknown binding is 404.
// These are enforced at the HTTP edge, so they are exercised through the real
// handler rather than the service alone.

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/api"
	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
)

func setupWebhookHandler(t *testing.T, config string) (*gin.Engine, int64, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, q := niutest.SetupDBRaw(t)
	ctx := context.Background()

	proj, err := q.CreateProject(ctx, store.CreateProjectParams{Name: "intake", OwnerType: "user", OwnerID: 1})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	kr, err := crypto.LoadOrCreate(t.TempDir() + "/integration_secret")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	const secret = "api-webhook-secret"
	creds := service.NewExternalCredentialService(q, db, kr, integration.NewRegistry())
	cred, err := creds.Create(ctx, service.ExternalCredentialUpsertInput{
		OwnerType: "user", OwnerID: 1, UserID: 1,
		Provider:  integration.ProviderName("webhook"),
		Alias:     "intake-api-test",
		RawConfig: map[string]any{"webhook_secret": secret},
	})
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	row, err := q.AddProjectExternalSource(ctx, store.AddProjectExternalSourceParams{
		ProjectID:    proj.ID,
		Provider:     "webhook",
		SourceKey:    "api-hooks",
		CredentialID: sql.NullInt64{Int64: cred.ID, Valid: true},
		Config:       config,
	})
	if err != nil {
		t.Fatalf("binding: %v", err)
	}

	svc := service.NewWebhookSourceService(db, q, creds)
	h := api.NewIntegrationWebhookHandler(svc)
	r := gin.New()
	r.POST("/api/integrations/webhook/:binding", h.Webhook)
	return r, row.ID, secret
}

func postWebhook(t *testing.T, r *gin.Engine, bindingID int64, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/api/integrations/webhook/"+strconv.FormatInt(bindingID, 10), strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestIntegrationWebhook_BodyCap(t *testing.T) {
	r, bindingID, _ := setupWebhookHandler(t, `{"max_body_bytes":64}`)

	if w := postWebhook(t, r, bindingID, strings.Repeat("a", 65), nil); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized payload status = %d, want 413", w.Code)
	}
	// Exactly at the cap passes the size gate and is then rejected for missing
	// authentication — 401, not 413.
	if w := postWebhook(t, r, bindingID, strings.Repeat("a", 64), nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("at-limit payload status = %d, want 401", w.Code)
	}
}

func TestIntegrationWebhook_RateLimitResponse(t *testing.T) {
	r, bindingID, secret := setupWebhookHandler(t, `{}`)
	auth := map[string]string{"Authorization": "Bearer " + secret}

	for i := 0; i < 60; i++ {
		if w := postWebhook(t, r, bindingID, `{}`, auth); w.Code == http.StatusTooManyRequests {
			t.Fatalf("request %d was rate limited before the budget was spent", i+1)
		}
	}
	w := postWebhook(t, r, bindingID, `{}`, auth)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("request after the budget status = %d, want 429", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1", got)
	}
}

// The CI route uses the same public auth contract: a wrong provider credential
// is 401 and records nothing; the correct one is acknowledged with 200.
func TestIntegrationWebhook_CIRouteAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, q := niutest.SetupDBRaw(t)
	ctx := context.Background()

	proj, err := q.CreateProject(ctx, store.CreateProjectParams{Name: "ci-intake", OwnerType: "user", OwnerID: 1})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	kr, err := crypto.LoadOrCreate(t.TempDir() + "/integration_secret")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	const secret = "ci-route-secret"
	creds := service.NewExternalCredentialService(q, db, kr, integration.NewRegistry())
	cred, err := creds.Create(ctx, service.ExternalCredentialUpsertInput{
		OwnerType: "user", OwnerID: 1, UserID: 1,
		Provider:  integration.ProviderName("ci"),
		Alias:     "intake-ci-api-test",
		RawConfig: map[string]any{"webhook_secret": secret},
	})
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	row, err := q.AddProjectExternalSource(ctx, store.AddProjectExternalSourceParams{
		ProjectID:    proj.ID,
		Provider:     "ci",
		SourceKey:    "pipeline",
		CredentialID: sql.NullInt64{Int64: cred.ID, Valid: true},
		Config:       `{"provider":"gitlab"}`,
	})
	if err != nil {
		t.Fatalf("binding: %v", err)
	}

	svc := service.NewWebhookSourceService(db, q, creds)
	h := api.NewIntegrationWebhookHandler(svc)
	r := gin.New()
	r.POST("/api/integrations/ci/:binding", h.CI)

	const body = `{"object_kind":"pipeline","object_attributes":{"id":1,"status":"failed","ref":"main","sha":"abc"},"project":{"path_with_namespace":"acme/api"},"user":{"id":7,"name":"Alice"}}`
	post := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost,
			"/api/integrations/ci/"+strconv.FormatInt(row.ID, 10), strings.NewReader(body))
		req.Header.Set("X-Gitlab-Token", token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	if w := post("wrong"); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong CI token status = %d, want 401", w.Code)
	}
	var events int
	if err := db.QueryRow(`SELECT COUNT(*) FROM intake_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("rejected CI delivery recorded %d events", events)
	}
	if w := post(secret); w.Code != http.StatusOK {
		t.Fatalf("valid CI token status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
}

// A local_only binding rejects a public peer with 403, even when the peer
// forges X-Forwarded-For: 127.0.0.1. The rejection happens before the pipeline,
// so no event is recorded.
func TestIntegrationWebhook_LocalOnlyForbidden(t *testing.T) {
	r, bindingID, secret := setupWebhookHandler(t, `{"local_only":true}`)

	// httptest.NewRequest defaults to 192.0.2.1, a public TEST-NET address.
	w := postWebhook(t, r, bindingID, `{"id":"evt-1","text":"hello"}`, map[string]string{
		"Authorization":   "Bearer " + secret,
		"X-Forwarded-For": "127.0.0.1",
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("public delivery status = %d, want 403 (body %s)", w.Code, w.Body.String())
	}
}

func TestIntegrationWebhook_StatusContract(t *testing.T) {
	r, bindingID, secret := setupWebhookHandler(t, `{}`)

	if w := postWebhook(t, r, bindingID, `{"id":"evt-1","text":"hello"}`, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned delivery status = %d, want 401", w.Code)
	}
	if w := postWebhook(t, r, 999999, `{"id":"evt-1","text":"hello"}`, map[string]string{
		"Authorization": "Bearer " + secret,
	}); w.Code != http.StatusNotFound {
		t.Fatalf("unknown binding status = %d, want 404", w.Code)
	}
}
