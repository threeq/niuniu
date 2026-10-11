// Integration tests for the provider pre-check / verify / publish flow (#732):
// PrecheckCall never touches the upstream; the verify probe runs against the
// provider's contract endpoint; a failed publish rolls back (nothing persisted).
package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/service"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
)

const probeContract = `{"schema_version":"1","provider":"probe",` +
	`"operations":[` +
	`{"name":"list","method":"GET","path":"/items","read_only":true},` +
	`{"name":"search","method":"GET","path":"/search","read_only":true,"parameters":[{"name":"q","in":"query","required":true}]}` +
	`],"verify_operation":"list"}`

// postProbeContract has a read-via-POST verify operation (lint warning, needs
// human confirmation at publish).
const postProbeContract = `{"schema_version":"1","provider":"probe",` +
	`"operations":[{"name":"search","method":"POST","path":"/search","read_only":true}],` +
	`"verify_operation":"search"}`

type verifyEnv struct {
	svc      *service.ExternalProxyService
	provSvc  *service.ExternalProviderService
	credSvc  *service.ExternalCredentialService
	userID   int64
	upstream *httptest.Server
	hits     *int32
}

func newVerifyEnv(t *testing.T, status int) *verifyEnv {
	t.Helper()
	env := niutest.NewIsolationEnv(t)
	kr, err := crypto.LoadOrCreate(env.TempPath(t, "integration_secret"))
	require.NoError(t, err)
	reg := integration.NewRegistry()
	credSvc := service.NewExternalCredentialService(env.Queries(), env.DB, kr, reg)
	provSvc := service.NewExternalProviderService(env.Queries(), env.DB)
	authz := service.NewAuthz(env.Queries(), env.DB)
	svc := service.NewExternalProxyService(env.Queries(), env.DB, provSvc, credSvc, authz, nil)

	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(upstream.Close)
	// httptest listens on loopback, which the production SSRF guard refuses — the
	// documented test seam swaps in the plain client.
	svc.SetClientForTest(upstream.Client())

	return &verifyEnv{svc: svc, provSvc: provSvc, credSvc: credSvc, userID: env.UserA, upstream: upstream, hits: &hits}
}

// seedProvider creates a provider pointed at the test upstream, wires the given
// contract as its profile, and binds a credential for the test user.
func (e *verifyEnv) seedProvider(t *testing.T, name, profile string, enabled bool) *service.ProviderDef {
	t.Helper()
	ctx := context.Background()
	created, err := e.provSvc.Create(ctx, name, name, e.upstream.URL)
	require.NoError(t, err)
	prov, err := e.provSvc.Update(ctx, created.ID, name, e.upstream.URL,
		"bearer", "Authorization", "Bearer", created.Whitelist, profile, "", enabled)
	require.NoError(t, err)
	_, err = e.credSvc.Create(ctx, service.ExternalCredentialUpsertInput{
		OwnerType: "user", OwnerID: e.userID, UserID: e.userID,
		Provider:  integration.ProviderName(name),
		Alias:     name + "-cred",
		RawConfig: map[string]any{"token": "tok"},
	})
	require.NoError(t, err)
	return prov
}

// The dry run validates locally and — proven by the upstream hit counter — never
// contacts the upstream, while a real call does.
func TestPrecheckCallDoesNotTouchUpstream(t *testing.T) {
	e := newVerifyEnv(t, http.StatusOK)
	e.seedProvider(t, "dryrun", probeContract, true)
	ctx := context.Background()

	// In-scope, params satisfied.
	pre, err := e.svc.PrecheckCall(ctx, service.ProxyInput{
		UserID: e.userID, Provider: "dryrun", Method: "GET", Path: "/items",
	})
	require.NoError(t, err)
	require.True(t, pre.OK)
	require.Equal(t, "list", pre.Operation)
	require.Equal(t, int32(0), atomic.LoadInt32(e.hits), "dry run must not contact the upstream")

	// Missing a contract-required parameter -> reported, still no upstream call.
	pre, err = e.svc.PrecheckCall(ctx, service.ProxyInput{
		UserID: e.userID, Provider: "dryrun", Method: "GET", Path: "/search",
	})
	require.NoError(t, err)
	require.False(t, pre.OK)
	require.Equal(t, int32(0), atomic.LoadInt32(e.hits))

	// Sanity: the injected client is live — a real call DOES reach the upstream.
	out, err := e.svc.Call(ctx, service.ProxyInput{
		UserID: e.userID, Provider: "dryrun", Method: "GET", Path: "/items",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, out.Status)
	require.Equal(t, int32(1), atomic.LoadInt32(e.hits))
}

// The dry run reports a value-free data summary of the body a write would send
// (Epic #726 / #747), so the write preview shows the DATA — field names + size +
// sensitive-pattern hits — not just the endpoint. Never touches the upstream.
func TestPrecheckCallReportsEgressDataSummary(t *testing.T) {
	e := newVerifyEnv(t, http.StatusOK)
	prov := e.seedProvider(t, "drysummary", probeContract, true)
	ctx := context.Background()
	require.NoError(t, e.provSvc.SetWritePref(ctx, e.userID, prov.ID, true))

	pre, err := e.svc.PrecheckCall(ctx, service.ProxyInput{
		UserID: e.userID, Provider: "drysummary", Method: "POST", Path: "/items",
		Body: map[string]any{"title": "hi", "password": "p@ss"},
	})
	require.NoError(t, err)
	require.NotNil(t, pre.DataSummary, "a write preview must carry a data summary")
	require.Equal(t, []string{"password", "title"}, pre.DataSummary.Fields)
	require.True(t, pre.DataSummary.HasSensitive(), "the password key must be flagged")
	require.NotNil(t, pre.EgressVerdict)
	require.Equal(t, int32(0), atomic.LoadInt32(e.hits), "the summary is derived locally")

	// A bodyless read carries no summary (nothing to describe).
	pre, err = e.svc.PrecheckCall(ctx, service.ProxyInput{
		UserID: e.userID, Provider: "drysummary", Method: "GET", Path: "/items",
	})
	require.NoError(t, err)
	require.Nil(t, pre.DataSummary)
}

// A successful publish runs the probe and enables the provider.
func TestPublishProviderSuccessEnables(t *testing.T) {
	e := newVerifyEnv(t, http.StatusOK)
	prov := e.seedProvider(t, "pubok", probeContract, false)
	ctx := context.Background()

	res, err := e.svc.PublishProvider(ctx, service.PublishProviderInput{
		ProviderID: prov.ID, UserID: e.userID,
	})
	require.NoError(t, err)
	require.True(t, res.OK)
	require.True(t, res.Published)
	require.False(t, res.RolledBack)
	require.Equal(t, http.StatusOK, res.Status)
	require.NotNil(t, res.Probe)
	require.Equal(t, "list", res.Probe.Operation)
	require.Equal(t, int32(1), atomic.LoadInt32(e.hits), "publish must run the probe once")

	after, err := e.provSvc.GetByID(ctx, prov.ID)
	require.NoError(t, err)
	require.True(t, after.Enabled, "a successful publish enables the provider")
}

// A failed probe rolls the publish back: nothing is persisted and the provider
// keeps its previous contract + disabled state.
func TestPublishProviderFailureRollsBack(t *testing.T) {
	e := newVerifyEnv(t, http.StatusInternalServerError)
	prov := e.seedProvider(t, "pubfail", "legacy free text", false)
	ctx := context.Background()

	res, err := e.svc.PublishProvider(ctx, service.PublishProviderInput{
		ProviderID: prov.ID, UserID: e.userID,
		Profile: strptr(probeContract),
	})
	require.NoError(t, err)
	require.False(t, res.OK)
	require.False(t, res.Published)
	require.True(t, res.RolledBack)
	require.Equal(t, http.StatusInternalServerError, res.Status)
	require.NotEmpty(t, res.NextAction)
	require.NotEmpty(t, res.Alternatives)

	after, err := e.provSvc.GetByID(ctx, prov.ID)
	require.NoError(t, err)
	require.False(t, after.Enabled, "rollback leaves the provider disabled")
	require.Equal(t, "legacy free text", after.Profile, "rollback leaves the previous profile intact")
}

// A non-GET probe is gated on explicit confirmation; the manifest is returned
// for the confirmation card, and the probe is NOT sent until it is confirmed.
func TestPublishProviderNonGetProbeRequiresConfirmation(t *testing.T) {
	e := newVerifyEnv(t, http.StatusOK)
	prov := e.seedProvider(t, "pubconfirm", postProbeContract, false)
	ctx := context.Background()

	res, err := e.svc.PublishProvider(ctx, service.PublishProviderInput{
		ProviderID: prov.ID, UserID: e.userID,
	})
	require.NoError(t, err)
	require.False(t, res.Published)
	require.True(t, res.RequiresConfirmation)
	require.True(t, res.RolledBack)
	require.NotNil(t, res.Probe)
	require.Equal(t, "POST", res.Probe.Method)
	require.False(t, res.Probe.CoarseReadOnly)
	require.Equal(t, int32(0), atomic.LoadInt32(e.hits), "an unconfirmed probe must not be sent")

	// After confirmation the probe runs and the provider publishes.
	res, err = e.svc.PublishProvider(ctx, service.PublishProviderInput{
		ProviderID: prov.ID, UserID: e.userID, Confirmed: true,
	})
	require.NoError(t, err)
	require.True(t, res.Published)
	require.Equal(t, int32(1), atomic.LoadInt32(e.hits))
}

func strptr(s string) *string { return &s }
