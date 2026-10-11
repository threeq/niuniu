// Integration tests for egress data governance on the provider line (Epic #726
// / #747): an over-threshold write raises a (value-free, warn-only) alert, and
// the audit READ side flags the same row — using the SAME policy threshold, so
// the two views never disagree.
package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/service"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
)

type recordingEgressNotifier struct {
	alerts []service.EgressAlert
}

func (n *recordingEgressNotifier) NotifyEgressAlert(_ context.Context, a service.EgressAlert) {
	n.alerts = append(n.alerts, a)
}

func TestExternalProxy_OversizeBodyWarnsAndFlags(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	kr, err := crypto.LoadOrCreate(env.TempPath(t, "integration_secret"))
	require.NoError(t, err)
	reg := integration.NewRegistry()
	credSvc := service.NewExternalCredentialService(env.Queries(), env.DB, kr, reg)
	provSvc := service.NewExternalProviderService(env.Queries(), env.DB)
	authz := service.NewAuthz(env.Queries(), env.DB)
	svc := service.NewExternalProxyService(env.Queries(), env.DB, provSvc, credSvc, authz)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	svc.SetClientForTest(upstream.Client())

	// A tiny ceiling so the test does not have to push a megabyte. The SAME
	// policy is wired to the audit reader, mirroring server.go.
	policy := service.DefaultEgressPolicy{MaxBytes: 32}
	svc.SetEgressPolicy(policy)
	notifier := &recordingEgressNotifier{}
	svc.SetEgressAlertNotifier(notifier)

	ctx := context.Background()
	prov, err := provSvc.Create(ctx, "egress_probe", "Egress Probe", upstream.URL)
	require.NoError(t, err)
	require.NoError(t, provSvc.SetWritePref(ctx, env.UserA, prov.ID, true))
	_, err = credSvc.Create(ctx, service.ExternalCredentialUpsertInput{
		OwnerType: "user", OwnerID: env.UserA, UserID: env.UserA,
		Provider:  integration.ProviderName("egress_probe"),
		Alias:     "default",
		RawConfig: map[string]any{"token": "tok", "auth_mode": "bearer"},
	})
	require.NoError(t, err)

	// A body over 32 bytes, and carrying a credential-shaped field.
	_, err = svc.Call(ctx, service.ProxyInput{
		UserID: env.UserA, Provider: "egress_probe", Method: "POST", Path: "/orders",
		Body: map[string]any{"note": strings.Repeat("x", 64), "api_key": "abcd"},
	})
	require.NoError(t, err, "the personal edition must still send the call")

	require.Len(t, notifier.alerts, 1, "an over-threshold write must raise exactly one alert")
	alert := notifier.alerts[0]
	require.Equal(t, service.EgressWarn, alert.Verdict.Decision)
	require.Equal(t, "egress_probe", alert.Provider)
	require.Equal(t, "user", alert.OwnerType)
	require.Equal(t, env.UserA, alert.OwnerID)
	require.Greater(t, alert.Summary.Bytes, int64(32))
	// The alert is value-free: no body value rides along.
	require.NotContains(t, alert.Verdict.Reasons, strings.Repeat("x", 64))

	// The read side flags the same row with the same threshold.
	auditSvc := service.NewIntegrationAuditService(env.Queries())
	auditSvc.SetEgressPolicy(policy)
	page, err := auditSvc.ListActivity(ctx, service.IntegrationActivityInput{UserID: env.UserA})
	require.NoError(t, err)
	require.NotEmpty(t, page.Items)
	require.Equal(t, service.ActivityKindAPI, page.Items[0].Kind)
	require.True(t, page.Items[0].Oversize, "the audit row must be flagged")

	// A second, small write does NOT alert.
	_, err = svc.Call(ctx, service.ProxyInput{
		UserID: env.UserA, Provider: "egress_probe", Method: "POST", Path: "/orders",
		Body: map[string]any{"ok": true},
	})
	require.NoError(t, err)
	require.Len(t, notifier.alerts, 1, "a small, clean body must not alert")
}
