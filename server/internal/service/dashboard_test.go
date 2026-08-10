// External tests for DashboardService — cover the read-only saved-query gate
// (write statements rejected), Pin auto-creating the owner's default dashboard
// (the nav-entry landing point), PanelData routing through the data-proxy gate,
// and owner filtering (a cross-tenant id is invisible).
package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/niuniu-dev/niuniu/internal/dataconn"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/service"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
)

// fakeDashboardProxy records calls and returns a fixed ResultSet so PanelData can be
// tested without a live database. It satisfies the dataProxyQuerier seam.
type fakeDashboardProxy struct {
	called int
	lastIn service.DataQueryInput
	rs     *dataconn.ResultSet
	err    error
}

func (p *fakeDashboardProxy) Query(ctx context.Context, in service.DataQueryInput) (*dataconn.ResultSet, error) {
	p.called++
	p.lastIn = in
	if p.err != nil {
		return nil, p.err
	}
	return p.rs, nil
}

type dashDeps struct {
	svc      *service.DashboardService
	dsSvc    *service.DataSourceService
	proxy    *fakeDashboardProxy
	sourceID int64
	uid      int64
	other    int64
	orgID    int64
}

func newTestDashboardService(t *testing.T) dashDeps {
	t.Helper()
	env := niutest.NewIsolationEnv(t)
	kr, err := crypto.LoadOrCreate(env.TempPath(t, "integration_secret"))
	if err != nil {
		t.Fatalf("LoadOrCreate keyring: %v", err)
	}
	reg := dataconn.NewRegistry()
	dsSvc := service.NewDataSourceService(env.Queries(), env.DB, kr, reg)

	uid := env.UserA
	sourceID, err := dsSvc.Create(context.Background(), service.CreateDataSourceInput{
		OwnerType: "user", OwnerID: uid, UserID: uid, Name: "analytics", Kind: "mysql",
		Config:            map[string]any{"host": "db", "port": 3306, "user": "ro", "password": "secret", "database": "analytics"},
		ScopeConfig:       map[string]any{},
		DefaultAccessMode: "read", RequireConfirm: "writes_only",
	})
	if err != nil {
		t.Fatalf("Create source: %v", err)
	}

	proxy := &fakeDashboardProxy{rs: &dataconn.ResultSet{
		Columns: []dataconn.Column{{Name: "id", Type: "number"}},
		Rows:    [][]any{{int64(1)}},
		Engine:  "mysql",
	}}
	svc := service.NewDashboardServiceWithSeams(env.Queries(), dsSvc, reg, proxy)
	return dashDeps{svc: svc, dsSvc: dsSvc, proxy: proxy, sourceID: sourceID, uid: uid, other: env.UserB, orgID: env.OrgA}
}

func TestSaveQueryRejectsWrite(t *testing.T) {
	d := newTestDashboardService(t)
	_, err := d.svc.SaveQuery(context.Background(), service.SaveQueryInput{
		OwnerType: "user", OwnerID: d.uid, UserID: d.uid, SourceID: d.sourceID, Name: "q",
		Operation: map[string]any{"statement": "DELETE FROM orders"},
	})
	if err == nil {
		t.Fatal("write statement must be rejected for saved query")
	}
	var notRO *service.ErrSavedQueryNotReadOnly
	if !errors.As(err, &notRO) {
		t.Fatalf("expected *ErrSavedQueryNotReadOnly, got %T: %v", err, err)
	}
}

func TestSaveQueryAcceptsRead(t *testing.T) {
	d := newTestDashboardService(t)
	id, err := d.svc.SaveQuery(context.Background(), service.SaveQueryInput{
		OwnerType: "user", OwnerID: d.uid, UserID: d.uid, SourceID: d.sourceID, Name: "q",
		Operation: map[string]any{"statement": "SELECT id FROM orders"},
		ChartSpec: map[string]any{"type": "bar"},
	})
	if err != nil || id == 0 {
		t.Fatalf("read statement should be saved: id=%d err=%v", id, err)
	}
}

// TestPanelDataThreadsNoSQLOperation verifies that a saved query backed by a
// NoSQL/HTTP source re-runs the FULL structured operation through the proxy (not
// just a SQL statement) — so redis/mongo/elasticsearch/http sources can back a
// live dashboard panel.
func TestPanelDataThreadsNoSQLOperation(t *testing.T) {
	d := newTestDashboardService(t)
	ctx := context.Background()

	// redis source + GET operation.
	redisID, err := d.dsSvc.Create(ctx, service.CreateDataSourceInput{
		OwnerType: "user", OwnerID: d.uid, UserID: d.uid, Name: "cache", Kind: "redis",
		Config: map[string]any{"host": "r", "port": 6379}, DefaultAccessMode: "read", RequireConfirm: "writes_only",
	})
	if err != nil {
		t.Fatalf("create redis: %v", err)
	}
	res, err := d.svc.Pin(ctx, service.PinInput{
		OwnerType: "user", OwnerID: d.uid, UserID: d.uid, SourceID: redisID, Name: "redis panel",
		Operation: map[string]any{"command": "GET", "args": []any{"cache:user:1"}},
		ChartSpec: map[string]any{"type": "table"},
	})
	if err != nil {
		t.Fatalf("pin redis op: %v", err)
	}
	if _, err := d.svc.PanelData(ctx, res.DashboardID, res.PanelID, d.uid, nil); err != nil {
		t.Fatalf("PanelData redis: %v", err)
	}
	if d.proxy.lastIn.Command != "GET" || len(d.proxy.lastIn.Args) != 1 || d.proxy.lastIn.Args[0] != "cache:user:1" {
		t.Fatalf("redis op not threaded to proxy: %+v", d.proxy.lastIn)
	}
	if d.proxy.lastIn.Statement != "" {
		t.Fatalf("redis panel must not carry a statement: %+v", d.proxy.lastIn)
	}

	// http source + GET operation (with query params).
	httpID, err := d.dsSvc.Create(ctx, service.CreateDataSourceInput{
		OwnerType: "user", OwnerID: d.uid, UserID: d.uid, Name: "api", Kind: "http",
		Config: map[string]any{"host": "api.example.com", "options": map[string]any{"scheme": "https"}},
		DefaultAccessMode: "read", RequireConfirm: "writes_only",
	})
	if err != nil {
		t.Fatalf("create http: %v", err)
	}
	res, err = d.svc.Pin(ctx, service.PinInput{
		OwnerType: "user", OwnerID: d.uid, UserID: d.uid, SourceID: httpID, Name: "http panel",
		Operation: map[string]any{"http_method": "GET", "http_path": "/v1/pets", "http_query": map[string]any{"status": "available"}, "http_list_path": "data.list"},
		ChartSpec: map[string]any{"type": "table"},
	})
	if err != nil {
		t.Fatalf("pin http op: %v", err)
	}
	if _, err := d.svc.PanelData(ctx, res.DashboardID, res.PanelID, d.uid, nil); err != nil {
		t.Fatalf("PanelData http: %v", err)
	}
	if d.proxy.lastIn.HTTPMethod != "GET" || d.proxy.lastIn.HTTPPath != "/v1/pets" {
		t.Fatalf("http op not threaded to proxy: %+v", d.proxy.lastIn)
	}
	if d.proxy.lastIn.HTTPQuery["status"] != "available" {
		t.Fatalf("http query not threaded: %+v", d.proxy.lastIn)
	}
	if d.proxy.lastIn.HTTPListPath != "data.list" {
		t.Fatalf("http_list_path not threaded to proxy: %+v", d.proxy.lastIn)
	}
}

// TestSaveQueryRejectsWriteOperation verifies the read-only gate covers
// structured (non-SQL) operations too: an http PUT (a write) is rejected.
func TestSaveQueryRejectsWriteOperation(t *testing.T) {
	d := newTestDashboardService(t)
	ctx := context.Background()
	httpID, err := d.dsSvc.Create(ctx, service.CreateDataSourceInput{
		OwnerType: "user", OwnerID: d.uid, UserID: d.uid, Name: "api", Kind: "http",
		Config: map[string]any{"host": "api.example.com"}, DefaultAccessMode: "read", RequireConfirm: "writes_only",
	})
	if err != nil {
		t.Fatalf("create http: %v", err)
	}
	_, err = d.svc.SaveQuery(ctx, service.SaveQueryInput{
		OwnerType: "user", OwnerID: d.uid, UserID: d.uid, SourceID: httpID, Name: "w",
		Operation: map[string]any{"http_method": "PUT", "http_path": "/v1/pets/1", "http_body": map[string]any{"name": "x"}},
	})
	var notRO *service.ErrSavedQueryNotReadOnly
	if !errors.As(err, &notRO) {
		t.Fatalf("expected *ErrSavedQueryNotReadOnly for http PUT, got %T: %v", err, err)
	}
}

// TestMoveAndCopyPanel covers moving a panel between dashboards (removed from
// source, present on target) and copying it (present on both).
func TestMoveAndCopyPanel(t *testing.T) {
	d := newTestDashboardService(t)
	ctx := context.Background()
	dash1, err := d.svc.CreateDashboard(ctx, service.CreateDashboardInput{OwnerType: "user", OwnerID: d.uid, Name: "A"})
	if err != nil {
		t.Fatalf("create dash1: %v", err)
	}
	dash2, err := d.svc.CreateDashboard(ctx, service.CreateDashboardInput{OwnerType: "user", OwnerID: d.uid, Name: "B"})
	if err != nil {
		t.Fatalf("create dash2: %v", err)
	}
	res, err := d.svc.Pin(ctx, service.PinInput{
		OwnerType: "user", OwnerID: d.uid, UserID: d.uid, SourceID: d.sourceID, DashboardID: dash1,
		Name: "p", Operation: map[string]any{"statement": "SELECT id FROM orders"}, ChartSpec: map[string]any{"type": "bar"},
	})
	if err != nil {
		t.Fatalf("pin: %v", err)
	}

	// Move dash1 -> dash2.
	if err := d.svc.MovePanel(ctx, dash1, res.PanelID, dash2, d.uid, nil); err != nil {
		t.Fatalf("move: %v", err)
	}
	p1, _ := d.svc.ListPanels(ctx, dash1, d.uid, nil)
	p2, _ := d.svc.ListPanels(ctx, dash2, d.uid, nil)
	if len(p1) != 0 || len(p2) != 1 {
		t.Fatalf("after move: dash1=%d dash2=%d want 0/1", len(p1), len(p2))
	}

	// Copy dash2 -> dash1: both have it now.
	newID, err := d.svc.CopyPanel(ctx, dash2, p2[0].ID, dash1, d.uid, nil)
	if err != nil || newID == 0 {
		t.Fatalf("copy: id=%d err=%v", newID, err)
	}
	p1, _ = d.svc.ListPanels(ctx, dash1, d.uid, nil)
	p2, _ = d.svc.ListPanels(ctx, dash2, d.uid, nil)
	if len(p1) != 1 || len(p2) != 1 {
		t.Fatalf("after copy: dash1=%d dash2=%d want 1/1", len(p1), len(p2))
	}

	// Move with the wrong source dashboard -> ErrPanelNotFound.
	if err := d.svc.MovePanel(ctx, dash1, p2[0].ID, dash2, d.uid, nil); !errors.Is(err, service.ErrPanelNotFound) {
		t.Fatalf("move with wrong src should be ErrPanelNotFound, got %v", err)
	}
}

// TestListPanelsForWorkspace covers the in-workspace data view: panels pinned
// from a workspace are listed with their dashboard name; other workspaces don't.
func TestListPanelsForWorkspace(t *testing.T) {
	d := newTestDashboardService(t)
	ctx := context.Background()
	const wsID = 4242
	res, err := d.svc.Pin(ctx, service.PinInput{
		OwnerType: "user", OwnerID: d.uid, UserID: d.uid, SourceID: d.sourceID, WorkspaceID: wsID,
		Name: "wp", Operation: map[string]any{"statement": "SELECT id FROM orders"}, ChartSpec: map[string]any{"type": "bar"},
	})
	if err != nil {
		t.Fatalf("pin: %v", err)
	}
	list, err := d.svc.ListPanelsForWorkspace(ctx, wsID, d.uid, nil)
	if err != nil || len(list) != 1 {
		t.Fatalf("ws panels=%d err=%v want 1", len(list), err)
	}
	if list[0].ID != res.PanelID || list[0].DashboardName == "" || list[0].SourceID != d.sourceID {
		t.Fatalf("ws panel dto wrong: %+v", list[0])
	}
	if other, _ := d.svc.ListPanelsForWorkspace(ctx, 9999, d.uid, nil); len(other) != 0 {
		t.Fatalf("other workspace should see no panels, got %d", len(other))
	}
}

func TestPinAutoCreatesDefaultDashboard(t *testing.T) {
	d := newTestDashboardService(t)
	ctx := context.Background()

	// No dashboard exists yet -> nav entry gated off.
	n, err := d.svc.CountDashboards(ctx, d.uid, nil)
	if err != nil || n != 0 {
		t.Fatalf("expected 0 dashboards before pin, got %d err=%v", n, err)
	}

	res, err := d.svc.Pin(ctx, service.PinInput{
		OwnerType: "user", OwnerID: d.uid, UserID: d.uid, SourceID: d.sourceID,
		WorkspaceID: 0, Name: "orders by day",
		Operation: map[string]any{"statement": "SELECT id FROM orders"},
		ChartSpec: map[string]any{"type": "bar"},
	})
	if err != nil {
		t.Fatalf("pin should succeed: %v", err)
	}
	if res.DashboardID == 0 || res.PanelID == 0 {
		t.Fatalf("pin must return dashboard+panel ids, got %+v", res)
	}

	// After first pin, the nav entry is now visible.
	n, err = d.svc.CountDashboards(ctx, d.uid, nil)
	if err != nil || n != 1 {
		t.Fatalf("expected 1 dashboard after pin, got %d err=%v", n, err)
	}

	// A second pin must reuse the same default dashboard, not create a new one.
	res2, err := d.svc.Pin(ctx, service.PinInput{
		OwnerType: "user", OwnerID: d.uid, UserID: d.uid, SourceID: d.sourceID,
		Name:      "second",
		Operation: map[string]any{"statement": "SELECT id FROM orders"},
	})
	if err != nil {
		t.Fatalf("second pin should succeed: %v", err)
	}
	if res2.DashboardID != res.DashboardID {
		t.Fatalf("second pin should reuse default dashboard %d, got %d", res.DashboardID, res2.DashboardID)
	}
	if n2, _ := d.svc.CountDashboards(ctx, d.uid, nil); n2 != 1 {
		t.Fatalf("still expected 1 dashboard after second pin, got %d", n2)
	}
}

func TestPanelDataGoesThroughProxyGate(t *testing.T) {
	d := newTestDashboardService(t)
	ctx := context.Background()

	res, err := d.svc.Pin(ctx, service.PinInput{
		OwnerType: "user", OwnerID: d.uid, UserID: d.uid, SourceID: d.sourceID,
		WorkspaceID: 0, Name: "p",
		Operation: map[string]any{"statement": "SELECT id FROM orders"},
	})
	if err != nil {
		t.Fatalf("pin: %v", err)
	}

	rs, err := d.svc.PanelData(ctx, res.DashboardID, res.PanelID, d.uid, nil)
	if err != nil {
		t.Fatalf("PanelData: %v", err)
	}
	if rs == nil || len(rs.Columns) != 1 {
		t.Fatalf("expected proxy ResultSet, got %+v", rs)
	}
	if d.proxy.called != 1 {
		t.Fatalf("PanelData must route through the data proxy exactly once, called=%d", d.proxy.called)
	}
	if d.proxy.lastIn.SourceID != d.sourceID || d.proxy.lastIn.Statement != "SELECT id FROM orders" {
		t.Fatalf("proxy input wrong: %+v", d.proxy.lastIn)
	}
	// I2: a personal saved query attributes to the user owner.
	if d.proxy.lastIn.OwnerType != "user" || d.proxy.lastIn.OwnerID != d.uid {
		t.Fatalf("personal panel must attribute to user owner, got %s/%d", d.proxy.lastIn.OwnerType, d.proxy.lastIn.OwnerID)
	}
}

// TestPanelDataThreadsOrgOwner verifies I2: an org-owned saved query's panel
// runs through the proxy with the saved query's REAL owner (org), not the
// calling user, so audit + confirmation attribute correctly.
func TestPanelDataThreadsOrgOwner(t *testing.T) {
	d := newTestDashboardService(t)
	ctx := context.Background()

	// An org-owned source the caller (UserA, a member of OrgA) can access.
	orgSourceID, err := d.dsSvc.Create(ctx, service.CreateDataSourceInput{
		OwnerType: "org", OwnerID: d.orgID, UserID: d.uid, Name: "org-analytics", Kind: "mysql",
		Config:            map[string]any{"host": "db", "port": 3306, "user": "ro", "password": "secret", "database": "analytics"},
		ScopeConfig:       map[string]any{},
		DefaultAccessMode: "read", RequireConfirm: "writes_only",
	})
	if err != nil {
		t.Fatalf("create org source: %v", err)
	}

	// Pin an org-owned saved query into an org-owned dashboard.
	res, err := d.svc.Pin(ctx, service.PinInput{
		OwnerType: "org", OwnerID: d.orgID, UserID: d.uid, OrgIDs: []int64{d.orgID},
		SourceID: orgSourceID, Name: "org panel",
		Operation: map[string]any{"statement": "SELECT id FROM orders"},
	})
	if err != nil {
		t.Fatalf("org pin: %v", err)
	}

	if _, err := d.svc.PanelData(ctx, res.DashboardID, res.PanelID, d.uid, []int64{d.orgID}); err != nil {
		t.Fatalf("PanelData (org): %v", err)
	}
	if d.proxy.lastIn.OwnerType != "org" || d.proxy.lastIn.OwnerID != d.orgID {
		t.Fatalf("org panel must attribute to org owner, got %s/%d", d.proxy.lastIn.OwnerType, d.proxy.lastIn.OwnerID)
	}
	// Source load still uses the caller's accessible set.
	if d.proxy.lastIn.UserID != d.uid {
		t.Fatalf("source load must keep caller userID, got %d", d.proxy.lastIn.UserID)
	}
}

// TestPinStaticChart verifies the static / direct-result chart path: pinning a
// chart with no backing data source (SourceID=0, an echarts chart_spec) saves a
// query with a NULL source_id (read-only gate skipped) and PanelData renders it
// from the stored snapshot WITHOUT invoking the data proxy.
func TestPinStaticChart(t *testing.T) {
	d := newTestDashboardService(t)
	ctx := context.Background()

	option := map[string]any{
		"xAxis":  map[string]any{"type": "category", "data": []any{"A", "B"}},
		"yAxis":  map[string]any{"type": "value"},
		"series": []any{map[string]any{"type": "bar", "data": []any{1, 2}}},
	}
	res, err := d.svc.Pin(ctx, service.PinInput{
		OwnerType: "user", OwnerID: d.uid, UserID: d.uid,
		SourceID: 0, // static: no backing data source
		Name:     "static echarts",
		ChartSpec: map[string]any{"type": "echarts", "option": option},
		// Snapshot left nil to exercise the empty-result path.
	})
	if err != nil {
		t.Fatalf("static pin should succeed: %v", err)
	}
	if res.DashboardID == 0 || res.PanelID == 0 {
		t.Fatalf("static pin must return dashboard+panel ids, got %+v", res)
	}

	// PanelData must NOT touch the proxy for a static panel and must return a
	// (here empty) ResultSet.
	rs, err := d.svc.PanelData(ctx, res.DashboardID, res.PanelID, d.uid, nil)
	if err != nil {
		t.Fatalf("PanelData (static): %v", err)
	}
	if rs == nil {
		t.Fatal("static PanelData must return a non-nil ResultSet")
	}
	if d.proxy.called != 0 {
		t.Fatalf("static PanelData must NOT call the data proxy, called=%d", d.proxy.called)
	}

	// The saved query DTO must report source_id 0 (NULL) for a static query.
	qs, err := d.svc.ListSavedQueries(ctx, d.uid, nil)
	if err != nil {
		t.Fatalf("ListSavedQueries: %v", err)
	}
	if len(qs) != 1 || qs[0].SourceID != 0 {
		t.Fatalf("static saved query must have source_id 0, got %+v", qs)
	}
}

// TestPinStaticChartWithSnapshot verifies that a static pin carrying an inline
// snapshot result is rendered back from PanelData (still without the proxy).
func TestPinStaticChartWithSnapshot(t *testing.T) {
	d := newTestDashboardService(t)
	ctx := context.Background()

	res, err := d.svc.Pin(ctx, service.PinInput{
		OwnerType: "user", OwnerID: d.uid, UserID: d.uid,
		SourceID:  0,
		Name:      "static with snapshot",
		ChartSpec: map[string]any{"type": "echarts", "option": map[string]any{}},
		Snapshot: map[string]any{
			"result": map[string]any{
				"columns":   []any{map[string]any{"name": "day", "type": "string"}},
				"rows":      []any{[]any{"mon"}},
				"truncated": false,
				"engine":    "",
			},
		},
	})
	if err != nil {
		t.Fatalf("static pin (snapshot) should succeed: %v", err)
	}

	rs, err := d.svc.PanelData(ctx, res.DashboardID, res.PanelID, d.uid, nil)
	if err != nil {
		t.Fatalf("PanelData (static snapshot): %v", err)
	}
	if rs == nil || len(rs.Columns) != 1 || rs.Columns[0].Name != "day" {
		t.Fatalf("snapshot result must be returned, got %+v", rs)
	}
	if d.proxy.called != 0 {
		t.Fatalf("static PanelData must NOT call the data proxy, called=%d", d.proxy.called)
	}
}

// TestPinStaticSnapshotRecordsQueriedAt verifies a static panel surfaces a
// "data time": an explicit snapshot queried_at is preserved, and a snapshot
// without one is stamped at pin time (so the field is never absent).
func TestPinStaticSnapshotRecordsQueriedAt(t *testing.T) {
	d := newTestDashboardService(t)
	ctx := context.Background()

	// 1) Explicit snapshot-level queried_at is round-tripped verbatim.
	want := "2026-06-20T08:30:00Z"
	res, err := d.svc.Pin(ctx, service.PinInput{
		OwnerType: "user", OwnerID: d.uid, UserID: d.uid,
		SourceID:  0,
		Name:      "snapshot with queried_at",
		ChartSpec: map[string]any{"type": "echarts", "option": map[string]any{}},
		Snapshot:  map[string]any{"queried_at": want},
	})
	if err != nil {
		t.Fatalf("static pin should succeed: %v", err)
	}
	rs, err := d.svc.PanelData(ctx, res.DashboardID, res.PanelID, d.uid, nil)
	if err != nil {
		t.Fatalf("PanelData: %v", err)
	}
	if rs.QueriedAt == nil || !rs.QueriedAt.Equal(mustParseTime(t, want)) {
		t.Fatalf("queried_at must round-trip, got %v want %s", rs.QueriedAt, want)
	}

	// 2) A snapshot without queried_at is stamped at pin time (never nil).
	res2, err := d.svc.Pin(ctx, service.PinInput{
		OwnerType: "user", OwnerID: d.uid, UserID: d.uid,
		SourceID:  0,
		Name:      "snapshot without queried_at",
		ChartSpec: map[string]any{"type": "echarts", "option": map[string]any{}},
	})
	if err != nil {
		t.Fatalf("static pin (no snapshot) should succeed: %v", err)
	}
	rs2, err := d.svc.PanelData(ctx, res2.DashboardID, res2.PanelID, d.uid, nil)
	if err != nil {
		t.Fatalf("PanelData: %v", err)
	}
	if rs2.QueriedAt == nil {
		t.Fatal("a static snapshot must always carry a queried_at (pin-time fallback)")
	}
}

func mustParseTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return v
}

func TestDashboardOwnerFiltering(t *testing.T) {
	d := newTestDashboardService(t)
	ctx := context.Background()

	res, err := d.svc.Pin(ctx, service.PinInput{
		OwnerType: "user", OwnerID: d.uid, UserID: d.uid, SourceID: d.sourceID, Name: "p",
		Operation: map[string]any{"statement": "SELECT id FROM orders"},
	})
	if err != nil {
		t.Fatalf("pin: %v", err)
	}

	// UserB (d.other) must not see UserA's dashboard / panel data.
	if _, err := d.svc.GetDashboard(ctx, res.DashboardID, d.other, nil); err == nil {
		t.Fatal("cross-tenant GetDashboard must fail")
	}
	if _, err := d.svc.PanelData(ctx, res.DashboardID, res.PanelID, d.other, nil); err == nil {
		t.Fatal("cross-tenant PanelData must fail")
	}
	if n, _ := d.svc.CountDashboards(ctx, d.other, nil); n != 0 {
		t.Fatalf("other user must count 0 dashboards, got %d", n)
	}
}
