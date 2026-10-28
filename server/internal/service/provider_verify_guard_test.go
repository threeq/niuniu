package service

// Defence-in-depth tests for the publish-time verification probe gate (Epic
// #726 / #732): a probe that is not provably read-only (GET/HEAD) must never be
// auto-executed on publish, even though the confirmation card was approved.
//
// Two layers are covered here:
//   - refuseNonCoarseProbe (the liveVerifier backstop) directly, and
//   - liveVerifier.VerifyProvider over the real provider/proxy stack, proving a
//     non-GET/HEAD-only contract results in no upstream request at all.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// The backstop refuses a non-coarse (non-GET/HEAD) manifest and surfaces the
// requires-confirmation signal; a provably read-only manifest is allowed to run.
func TestRefuseNonCoarseProbe(t *testing.T) {
	post := BuildProbeManifest(
		&ContractOperation{Name: "transfer", Method: "POST", Path: "/transfer", ReadOnly: true},
		"verify_operation")
	res, refused := refuseNonCoarseProbe(post)
	require.True(t, refused, "a non-GET/HEAD probe must be refused")
	require.NotNil(t, res)
	require.False(t, res.OK)
	require.True(t, res.RequiresConfirmation)
	require.NotNil(t, res.Probe)
	require.Equal(t, "POST", res.Probe.Method)
	require.NotEmpty(t, res.Detail)

	get := BuildProbeManifest(
		&ContractOperation{Name: "ping", Method: "GET", Path: "/ping", ReadOnly: true},
		"verify_operation")
	allowed, refused := refuseNonCoarseProbe(get)
	require.False(t, refused, "a GET probe is provably read-only and may run")
	require.Nil(t, allowed)
}

// End to end: a provider whose contract exposes only a read-via-POST probe is
// never probed — liveVerifier refuses and makes zero upstream requests, so a
// mislabelled write cannot fire on publish.
func TestLiveVerifierDoesNotExecuteNonReadOnlyProbe(t *testing.T) {
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	prev := KBAllowLocalSources()
	SetKBAllowLocalSources(true)
	t.Cleanup(func() { SetKBAllowLocalSources(prev) })

	// Real schema + real provider/proxy stack (no sandbox helper needed).
	db := newFnTestDB(t)
	q := store.NewQueries(db)
	providers := NewExternalProviderService(q, db)
	credSvc := NewExternalCredentialService(q, db, testKeyring(t), integration.NewRegistry())
	proxy := NewExternalProxyService(q, db, providers, credSvc, nil, nil)
	proxy.SetClientForTest(upstream.Client())
	v := &liveVerifier{proxy: proxy, providers: providers}

	// A contract whose only verify_operation is a read-via-POST.
	const postOnly = `{"schema_version":"1","provider":"postonly",` +
		`"operations":[{"name":"search","method":"POST","path":"/search","read_only":true}],` +
		`"verify_operation":"search"}`
	ctx := context.Background()
	created, err := providers.Create(ctx, "postonly", "postonly", upstream.URL)
	require.NoError(t, err)
	_, err = providers.Update(ctx, created.ID, "postonly", upstream.URL,
		"bearer", "Authorization", "Bearer", created.Whitelist, postOnly, "", false)
	require.NoError(t, err)

	res, err := v.VerifyProvider(ctx, "postonly", 1)
	require.NoError(t, err)
	require.False(t, res.OK, "a contract with no GET/HEAD probe must not verify as OK")
	require.Equal(t, int32(0), atomic.LoadInt32(&hits),
		"a non-provably-read-only probe must never be executed on publish")
}
