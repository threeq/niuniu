// End-to-end tests for the continuous integration-source health sweep
// (Epic #726 / #744): a failing source is auto-disabled after N consecutive
// failures and alerted, then auto-recovers when it probes clean again; an
// upstream OpenAPI document change is detected as contract drift and can be
// acknowledged. External (service_test) package so the niutest IsolationEnv
// fixtures are usable.
package service_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/dataconn"
	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
)

// switchableConnector is a dataconn.Connector whose Ping result is switchable, so a
// test can simulate an upstream going down and coming back.
type switchableConnector struct {
	mu      sync.Mutex
	pingErr error
}

func (c *switchableConnector) Kind() dataconn.SourceKind { return dataconn.KindMySQL }

func (c *switchableConnector) Ping(ctx context.Context, conn dataconn.ConnConfig) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pingErr
}

func (c *switchableConnector) Classify(op dataconn.Operation) (dataconn.AccessMode, dataconn.ResourceRef, error) {
	return dataconn.ModeRead, dataconn.ResourceRef{}, nil
}

func (c *switchableConnector) Execute(ctx context.Context, conn dataconn.ConnConfig, op dataconn.Operation) (*dataconn.ResultSet, error) {
	return &dataconn.ResultSet{}, nil
}

func (c *switchableConnector) setErr(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pingErr = err
}

// recordingNotifier captures the alerts the sweep emits.
type recordingNotifier struct {
	mu     sync.Mutex
	alerts []service.IntegrationHealthAlert
}

func (n *recordingNotifier) NotifyIntegrationHealth(ctx context.Context, a service.IntegrationHealthAlert) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.alerts = append(n.alerts, a)
}

func (n *recordingNotifier) kinds() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, len(n.alerts))
	for i, a := range n.alerts {
		out[i] = a.Kind
	}
	return out
}

// TestHealthSweep_DataSourceAutoDisableAndRecover drives the full data-source
// path: two consecutive Ping failures auto-disable the source and fire one
// alert; a subsequent clean Ping auto-recovers it and fires a second.
func TestHealthSweep_DataSourceAutoDisableAndRecover(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	ctx := context.Background()
	kr, err := crypto.LoadOrCreate(env.TempPath(t, "integration_secret"))
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}

	conn := &switchableConnector{}
	reg := dataconn.NewRegistry()
	reg.Register(dataconn.KindMySQL, conn)

	dsSvc := service.NewDataSourceService(env.Queries(), env.DB, kr, reg)
	uid := env.UserA
	id, err := dsSvc.Create(ctx, service.CreateDataSourceInput{
		OwnerType: "user", OwnerID: uid, UserID: uid, Name: "analytics", Kind: "mysql",
		Config:            map[string]any{"host": "db", "port": 3306, "user": "ro", "password": "x", "database": "d"},
		ScopeConfig:       map[string]any{},
		DefaultAccessMode: "read", RequireConfirm: "writes_only",
	})
	if err != nil {
		t.Fatalf("create data source: %v", err)
	}

	health := service.NewIntegrationHealthService(
		env.Queries(), env.DB, nil, nil, dsSvc,
		service.IntegrationHealthConfig{Enabled: true, FailureThreshold: 2},
	)
	notifier := &recordingNotifier{}
	health.SetNotifier(notifier)

	// Failure 1: unhealthy, still enabled.
	conn.setErr(errors.New("connection refused"))
	out, err := health.ProbeDataSource(ctx, id)
	if err != nil {
		t.Fatalf("probe 1: %v", err)
	}
	if out.Status != service.ProbeStatusFailed || out.ConsecutiveFailures != 1 || out.AutoDisabled {
		t.Fatalf("probe 1 outcome: %+v", out)
	}

	// Failure 2 (== threshold): auto-disabled.
	out, err = health.ProbeDataSource(ctx, id)
	if err != nil {
		t.Fatalf("probe 2: %v", err)
	}
	if !out.AutoDisabled || out.ConsecutiveFailures != 2 {
		t.Fatalf("probe 2 should auto-disable: %+v", out)
	}
	row := loadDataSource(t, env, id)
	if row.Enabled != 0 || row.HealthStatus != service.HealthDisabled || !row.AutoDisabledAt.Valid {
		t.Fatalf("row after auto-disable: enabled=%d health=%s autoAtValid=%v", row.Enabled, row.HealthStatus, row.AutoDisabledAt.Valid)
	}
	if got := notifier.kinds(); len(got) != 1 || got[0] != service.AlertAutoDisabled {
		t.Fatalf("alerts after auto-disable: %v", got)
	}

	// Recovery: a clean Ping auto-re-enables the source.
	conn.setErr(nil)
	out, err = health.ProbeDataSource(ctx, id)
	if err != nil {
		t.Fatalf("probe 3: %v", err)
	}
	if out.Status != service.ProbeStatusOK || out.ConsecutiveFailures != 0 {
		t.Fatalf("probe 3 should be ok: %+v", out)
	}
	row = loadDataSource(t, env, id)
	if row.Enabled != 1 || row.HealthStatus != service.HealthHealthy || row.AutoDisabledAt.Valid {
		t.Fatalf("row after recovery: enabled=%d health=%s autoAtValid=%v", row.Enabled, row.HealthStatus, row.AutoDisabledAt.Valid)
	}
	if got := notifier.kinds(); len(got) != 2 || got[1] != service.AlertRecovered {
		t.Fatalf("alerts after recovery: %v", got)
	}

	// Manual re-enable is a no-op on an already-enabled source (recovery path
	// stays usable): it must not error.
	if err := health.Reenable(ctx, service.ProbeSubjectDataSource, id); err != nil {
		t.Fatalf("reenable: %v", err)
	}

	// Probe history is recorded (the audit trail).
	runs, err := health.ListProbeRuns(ctx, service.ProbeSubjectDataSource, id, uid, 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("expected 3 probe runs, got %d", len(runs))
	}
	if runs[0].Status != service.ProbeStatusOK {
		t.Fatalf("newest run should be ok, got %s", runs[0].Status)
	}

	// IDOR guard: another user must not see this source's probe history (the
	// run messages can quote a raw driver error with host/port/user).
	other, err := health.ListProbeRuns(ctx, service.ProbeSubjectDataSource, id, env.UserB, 10)
	if err != nil {
		t.Fatalf("list runs as other user: %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("probe history leaked to another user: %d runs", len(other))
	}
}

func loadDataSource(t *testing.T, env *niutest.IsolationEnv, id int64) store.DataSource {
	t.Helper()
	row, err := env.Queries().GetDataSource(context.Background(), id)
	if err != nil {
		t.Fatalf("get data source: %v", err)
	}
	return row
}

// TestHealthSweep_ProviderDriftDetectedAndAcknowledged exercises the provider
// line: a healthy verify_operation probe, then an upstream OpenAPI document
// change detected as drift, then acknowledgement re-baselining it.
func TestHealthSweep_ProviderDriftDetectedAndAcknowledged(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	ctx := context.Background()
	kr, err := crypto.LoadOrCreate(env.TempPath(t, "integration_secret"))
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}

	// Upstream: a verify endpoint + an OpenAPI document whose content the test
	// can swap to simulate an upstream change.
	var spec atomic.Value
	spec.Store(`{"openapi":"3.0.0","info":{"title":"v1"}}`)
	mux := http.NewServeMux()
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(spec.Load().(string)))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	credReg := integration.NewRegistry()
	credSvc := service.NewExternalCredentialService(env.Queries(), env.DB, kr, credReg)
	provSvc := service.NewExternalProviderService(env.Queries(), env.DB)
	authz := service.NewAuthz(env.Queries(), env.DB)
	proxySvc := service.NewExternalProxyService(env.Queries(), env.DB, provSvc, credSvc, authz)
	proxySvc.SetClientForTest(srv.Client()) // httptest listens on loopback

	uid := env.UserA
	prov, err := provSvc.Create(ctx, "healthprov", "Health Prov", srv.URL)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	profile := `{"schema_version":"1","verify_operation":"search","operations":[{"name":"search","method":"GET","path":"/search","read_only":true}]}`
	if _, err := provSvc.Update(ctx, prov.ID, "Health Prov", srv.URL, "bearer", "Authorization", "Bearer",
		"", profile, srv.URL+"/openapi.json", true); err != nil {
		t.Fatalf("update provider: %v", err)
	}
	if _, err := credSvc.Create(ctx, service.ExternalCredentialUpsertInput{
		OwnerType: "user", OwnerID: uid, UserID: uid,
		Provider: integration.ProviderName("healthprov"), Alias: "default",
		RawConfig: map[string]any{"token": "t", "auth_mode": "bearer"},
	}); err != nil {
		t.Fatalf("create credential: %v", err)
	}
	// Allow the (read-only) probe through the write gate.
	if err := provSvc.SetWritePref(ctx, uid, prov.ID, true); err != nil {
		t.Fatalf("set write pref: %v", err)
	}

	dsSvc := service.NewDataSourceService(env.Queries(), env.DB, kr, dataconn.NewRegistry())
	health := service.NewIntegrationHealthService(env.Queries(), env.DB, provSvc, proxySvc, dsSvc,
		service.IntegrationHealthConfig{Enabled: true, FailureThreshold: 3})
	health.SetClientForTest(srv.Client())
	notifier := &recordingNotifier{}
	health.SetNotifier(notifier)

	// First probe: ok, and the drift baseline is established (no drift).
	out, err := health.ProbeProvider(ctx, prov.ID)
	if err != nil {
		t.Fatalf("probe 1: %v", err)
	}
	if out.Status != service.ProbeStatusOK || out.Drift {
		t.Fatalf("probe 1 outcome: %+v", out)
	}

	// Upstream document changes -> drift on the next probe.
	spec.Store(`{"openapi":"3.0.0","info":{"title":"v2-changed"}}`)
	out, err = health.ProbeProvider(ctx, prov.ID)
	if err != nil {
		t.Fatalf("probe 2: %v", err)
	}
	if !out.Drift {
		t.Fatalf("probe 2 should detect drift: %+v", out)
	}
	if got := notifier.kinds(); len(got) != 1 || got[0] != service.AlertDrift {
		t.Fatalf("alerts after drift: %v", got)
	}
	row, err := env.Queries().GetProviderByID(ctx, prov.ID)
	if err != nil {
		t.Fatalf("get provider: %v", err)
	}
	if row.ContractDrift != 1 {
		t.Fatalf("contract_drift flag should be set, got %d", row.ContractDrift)
	}

	// A further upstream change while still flagged must NOT clear the flag
	// (the user has not acknowledged yet).
	spec.Store(`{"openapi":"3.0.0","info":{"title":"v3-again"}}`)
	out, err = health.ProbeProvider(ctx, prov.ID)
	if err != nil {
		t.Fatalf("probe 2b: %v", err)
	}
	if !out.Drift {
		t.Fatalf("probe 2b should still report drift: %+v", out)
	}
	row, _ = env.Queries().GetProviderByID(ctx, prov.ID)
	if row.ContractDrift != 1 {
		t.Fatalf("contract_drift must stay set until acknowledged, got %d", row.ContractDrift)
	}
	if got := notifier.kinds(); len(got) != 1 {
		t.Fatalf("already-flagged drift must not re-notify: %v", got)
	}

	// Acknowledge re-baselines: flag clears and does not re-fire.
	if err := health.AcknowledgeDrift(ctx, prov.ID); err != nil {
		t.Fatalf("acknowledge drift: %v", err)
	}
	out, err = health.ProbeProvider(ctx, prov.ID)
	if err != nil {
		t.Fatalf("probe 3: %v", err)
	}
	if out.Drift {
		t.Fatalf("probe 3 should not re-detect drift: %+v", out)
	}
	row, _ = env.Queries().GetProviderByID(ctx, prov.ID)
	if row.ContractDrift != 0 {
		t.Fatalf("contract_drift should be cleared, got %d", row.ContractDrift)
	}
}
