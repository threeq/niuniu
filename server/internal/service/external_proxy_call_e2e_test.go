// End-to-end test for the whole external-proxy path (issue #737): provider +
// credential resolved from the store, operation-level authorization applied at
// egress, the credential injected by the resolved strategy, and the HTTP call
// executed against a live upstream. This is the integration counterpart to the
// pure authorizeCall / strategy unit tests.
//
// Package service_test (external) so the shared niutest IsolationEnv fixtures
// are usable; the SSRF-guarded production client refuses loopback (where the
// httptest upstream listens), so the plain client is injected via
// SetClientForTest.
package service_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/service"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
)

// callE2EContract declares one read-only and one write operation, no base_url
// (the provider row supplies it).
const callE2EContract = `{
  "schema_version": "1",
  "provider": "OrderShop",
  "auth": {"strategy": "bearer"},
  "operations": [
    {"name": "checkOrder", "method": "GET", "path": "/orders/{orderId}", "read_only": true},
    {"name": "cancelOrder", "method": "POST", "path": "/orders/{orderId}/cancel", "read_only": false}
  ]
}`

func TestExternalProxy_Call_E2E(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	kr, err := crypto.LoadOrCreate(env.TempPath(t, "integration_secret"))
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	provSvc := service.NewExternalProviderService(env.Queries(), env.DB)
	credSvc := service.NewExternalCredentialService(env.Queries(), env.DB, kr, integration.NewRegistry())
	authz := service.NewAuthz(env.Queries(), env.DB)
	svc := service.NewExternalProxyService(env.Queries(), env.DB, provSvc, credSvc, authz)

	// Upstream that only accepts a known bearer token and records what it saw.
	var seenAuth, seenPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		seenPath = r.URL.Path
		if seenAuth != "Bearer tok-e2e" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	svc.SetClientForTest(upstream.Client())

	ctx := context.Background()
	prov, err := provSvc.Create(ctx, "ordershop", "OrderShop", upstream.URL)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if _, err := provSvc.Update(ctx, prov.ID, "OrderShop", upstream.URL,
		"bearer", "Authorization", "Bearer",
		`[{"method":"GET","path":"*"}]`, callE2EContract, "", true); err != nil {
		t.Fatalf("update provider: %v", err)
	}
	if _, err := credSvc.Create(ctx, service.ExternalCredentialUpsertInput{
		OwnerType: "user", OwnerID: env.UserA, UserID: env.UserA,
		Provider:  integration.ProviderName("ordershop"),
		Alias:     "ordershop-e2e",
		RawConfig: map[string]any{"auth_mode": "bearer", "token": "tok-e2e"},
	}); err != nil {
		t.Fatalf("create credential: %v", err)
	}

	// (1) A read-only operation resolves the path, is authorized, and the
	// credential is injected by the static strategy end-to-end.
	out, err := svc.Call(ctx, service.ProxyInput{
		UserID:    env.UserA,
		Provider:  "ordershop",
		Operation: "checkOrder",
		Query:     map[string]any{"orderId": "42"},
	})
	if err != nil {
		t.Fatalf("read-only operation call failed: %v", err)
	}
	if out.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200", out.Status)
	}
	if seenAuth != "Bearer tok-e2e" {
		t.Fatalf("upstream saw Authorization %q, want Bearer tok-e2e", seenAuth)
	}
	if seenPath != "/orders/42" {
		t.Fatalf("upstream saw path %q, want /orders/42 (path param folded in)", seenPath)
	}

	// (2) A write operation is refused at egress with a reason naming it.
	_, err = svc.Call(ctx, service.ProxyInput{
		UserID:    env.UserA,
		Provider:  "ordershop",
		Operation: "cancelOrder",
		Query:     map[string]any{"orderId": "42"},
	})
	var pe *service.ProxyError
	if !errors.As(err, &pe) || pe.Code != "operation_requires_write" {
		t.Fatalf("write operation must be refused with operation_requires_write, got %v", err)
	}
	if !strings.Contains(pe.Message, "cancelOrder") {
		t.Fatalf("refusal reason must name the operation, got %q", pe.Message)
	}

	// (3) An unknown operation is refused with a clear, actionable reason.
	_, err = svc.Call(ctx, service.ProxyInput{
		UserID:    env.UserA,
		Provider:  "ordershop",
		Operation: "dropDatabase",
	})
	if !errors.As(err, &pe) || pe.Code != "unknown_operation" {
		t.Fatalf("unknown operation must be refused with unknown_operation, got %v", err)
	}
	if !strings.Contains(pe.Message, "dropDatabase") ||
		!strings.Contains(pe.Message, "get_provider_schema") {
		t.Fatalf("reason must name the operation and point at the schema: %q", pe.Message)
	}

	// (4) Legacy method+path addressing still works (zero regression).
	out, err = svc.Call(ctx, service.ProxyInput{
		UserID:   env.UserA,
		Provider: "ordershop",
		Method:   "GET",
		Path:     "/orders/9",
	})
	if err != nil || out.Status != http.StatusOK {
		t.Fatalf("legacy method+path call failed: status=%v err=%v", out, err)
	}
}
