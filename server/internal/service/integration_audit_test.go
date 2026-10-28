// Tests for the integration activity feed (Epic #726 / #728): the read side of
// external_api_audit + data_source_audit, and the body fingerprint the proxy
// writes into the former.
package service_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/service"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
)

// seedActivity inserts one row into each audit table per call, with an explicit
// created_at so the merge order and the time filters are deterministic. Rows
// are written with raw SQL because the production write path always stamps
// CURRENT_TIMESTAMP.
func seedAPIAudit(t *testing.T, db *sql.DB, userID, providerID int64, method, path, created string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO external_api_audit (user_id, provider_id, method, path, status_code, created_at)
		 VALUES (?, ?, ?, ?, 200, ?)`,
		userID, providerID, method, path, created)
	if err != nil {
		t.Fatalf("seed api audit: %v", err)
	}
}

func seedDataAudit(t *testing.T, db *sql.DB, userID, sourceID int64, summary, created string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO data_source_audit (user_id, source_id, access_mode, database_name, objects, statement_summary, status, "rows", duration_ms, created_at)
		 VALUES (?, ?, 'read', 'app', '[]', ?, 'ok', 3, 12, ?)`,
		userID, sourceID, summary, created)
	if err != nil {
		t.Fatalf("seed data audit: %v", err)
	}
}

// activityFixture seeds a fixed, interleaved timeline across both tables:
//
//	2026-10-05 10:00  api  provider 10   (newest)
//	2026-10-04 10:00  data source   21
//	2026-10-03 10:00  api  provider 11
//	2026-10-02 10:00  data source   20
//	2026-10-01 10:00  api  provider 10   (oldest)
func activityFixture(t *testing.T) (*sql.DB, *service.IntegrationAuditService, int64) {
	t.Helper()
	db, q := niutest.SetupDBRaw(t)
	const uid int64 = 7

	seedAPIAudit(t, db, uid, 10, "GET", "/repos/a", "2026-10-01 10:00:00")
	seedDataAudit(t, db, uid, 20, "SELECT 1", "2026-10-02 10:00:00")
	seedAPIAudit(t, db, uid, 11, "POST", "/repos/b", "2026-10-03 10:00:00")
	seedDataAudit(t, db, uid, 21, "SELECT 2", "2026-10-04 10:00:00")
	seedAPIAudit(t, db, uid, 10, "DELETE", "/repos/c", "2026-10-05 10:00:00")

	// A different user's rows must never surface in this user's feed.
	seedAPIAudit(t, db, 999, 10, "GET", "/other", "2026-10-06 10:00:00")
	seedDataAudit(t, db, 999, 20, "SELECT other", "2026-10-06 11:00:00")

	// Names come from the owning tables, not the audit rows. Explicit ids so
	// they line up with the provider_id/source_id stamped above.
	if _, err := db.Exec(
		`INSERT INTO external_providers (id, name, api_base_url) VALUES (10, 'prov-ten', 'https://a.example'), (11, 'prov-eleven', 'https://b.example')`,
	); err != nil {
		t.Fatalf("seed providers: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO data_sources (id, owner_type, owner_id, user_id, name, kind) VALUES (20, 'user', ?, ?, 'warehouse', 'postgres')`,
		uid, uid,
	); err != nil {
		t.Fatalf("seed data source: %v", err)
	}

	return db, service.NewIntegrationAuditService(q), uid
}

func pathsOf(items []service.IntegrationActivityItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		switch it.Kind {
		case service.ActivityKindAPI:
			out = append(out, "api:"+it.Path)
		case service.ActivityKindData:
			out = append(out, "data:"+it.StatementSummary)
		}
	}
	return out
}

func TestIntegrationActivity_NewestFirstAndPaged(t *testing.T) {
	_, svc, uid := activityFixture(t)
	ctx := context.Background()

	// Walk the whole feed two at a time; the concatenation of the pages must be
	// the full timeline, with no row duplicated or dropped at a boundary.
	want := []string{"api:/repos/c", "data:SELECT 2", "api:/repos/b", "data:SELECT 1", "api:/repos/a"}
	var got []string
	for offset := int64(0); offset < 6; offset += 2 {
		page, err := svc.ListActivity(ctx, service.IntegrationActivityInput{
			UserID: uid, Limit: 2, Offset: offset,
		})
		if err != nil {
			t.Fatalf("offset %d: %v", offset, err)
		}
		if page.Total != 5 {
			t.Errorf("offset %d: total = %d, want 5 (other users' rows must not count)", offset, page.Total)
		}
		got = append(got, pathsOf(page.Items)...)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("paged feed =\n %v\nwant\n %v", got, want)
	}

	// Past the end: empty page, total unchanged.
	page, err := svc.ListActivity(ctx, service.IntegrationActivityInput{UserID: uid, Limit: 2, Offset: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 || page.Total != 5 {
		t.Errorf("past-the-end page = %d items / total %d, want 0 / 5", len(page.Items), page.Total)
	}
	if page.Items == nil {
		t.Error("items must marshal as [] rather than null")
	}
}

func TestIntegrationActivity_KindAndSourceFilters(t *testing.T) {
	_, svc, uid := activityFixture(t)
	ctx := context.Background()

	cases := []struct {
		name string
		in   service.IntegrationActivityInput
		want []string
	}{
		{
			name: "kind=api",
			in:   service.IntegrationActivityInput{UserID: uid, Kind: service.ActivityKindAPI},
			want: []string{"api:/repos/c", "api:/repos/b", "api:/repos/a"},
		},
		{
			name: "kind=data",
			in:   service.IntegrationActivityInput{UserID: uid, Kind: service.ActivityKindData},
			want: []string{"data:SELECT 2", "data:SELECT 1"},
		},
		{
			name: "provider narrows to that provider's API calls",
			in:   service.IntegrationActivityInput{UserID: uid, ProviderID: 10},
			want: []string{"api:/repos/c", "api:/repos/a"},
		},
		{
			name: "source narrows to that source's data queries",
			in:   service.IntegrationActivityInput{UserID: uid, SourceID: 21},
			want: []string{"data:SELECT 2"},
		},
		{
			name: "unknown id yields nothing",
			in:   service.IntegrationActivityInput{UserID: uid, ProviderID: 4242},
			want: []string{},
		},
		{
			// A provider filter can only remove the data side; it must not
			// override an explicit kind=data and start returning api rows.
			name: "kind=data with a provider filter is empty, not api rows",
			in:   service.IntegrationActivityInput{UserID: uid, Kind: service.ActivityKindData, ProviderID: 10},
			want: []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, err := svc.ListActivity(ctx, tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if got := pathsOf(page.Items); fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("items = %v, want %v", got, tc.want)
			}
			if page.Total != int64(len(tc.want)) {
				t.Errorf("total = %d, want %d", page.Total, len(tc.want))
			}
		})
	}
}

func TestIntegrationActivity_TimeWindow(t *testing.T) {
	_, svc, uid := activityFixture(t)
	ctx := context.Background()

	page, err := svc.ListActivity(ctx, service.IntegrationActivityInput{
		UserID: uid,
		From:   time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
		To:     time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"data:SELECT 2", "api:/repos/b", "data:SELECT 1"}
	if got := pathsOf(page.Items); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("windowed items = %v, want %v", got, want)
	}
	if page.Total != 3 {
		t.Errorf("total = %d, want 3", page.Total)
	}

	// An open-ended lower bound (no `to`) keeps everything after `from`.
	page, err = svc.ListActivity(ctx, service.IntegrationActivityInput{
		UserID: uid,
		From:   time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := pathsOf(page.Items); fmt.Sprint(got) != fmt.Sprint([]string{"api:/repos/c", "data:SELECT 2"}) {
		t.Errorf("open-ended items = %v", got)
	}
}

func TestIntegrationActivity_RejectsUnknownKind(t *testing.T) {
	_, svc, uid := activityFixture(t)
	_, err := svc.ListActivity(context.Background(), service.IntegrationActivityInput{UserID: uid, Kind: "bogus"})
	if err != service.ErrBadActivityKind {
		t.Fatalf("err = %v, want ErrBadActivityKind", err)
	}
}

func TestIntegrationActivity_ResolvesNamesAndCarriesKindFields(t *testing.T) {
	_, svc, uid := activityFixture(t)

	page, err := svc.ListActivity(context.Background(), service.IntegrationActivityInput{UserID: uid, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}

	var sawProvider, sawSource bool
	for _, it := range page.Items {
		switch it.Kind {
		case service.ActivityKindAPI:
			if it.Method == "" || it.Path == "" || it.StatusCode == nil {
				t.Errorf("api item missing core fields: %+v", it)
			}
			if it.ProviderName == "prov-ten" || it.ProviderName == "prov-eleven" {
				sawProvider = true
			}
			// The data-only columns must stay out of an api row.
			if it.SourceID != nil || it.StatementSummary != "" {
				t.Errorf("api item carries data fields: %+v", it)
			}
		case service.ActivityKindData:
			if it.AccessMode == "" || it.Status == "" || it.Rows == nil || it.DurationMs == nil {
				t.Errorf("data item missing core fields: %+v", it)
			}
			if it.SourceName == "warehouse" {
				sawSource = true
			}
			if it.ProviderID != nil || it.Path != "" {
				t.Errorf("data item carries api fields: %+v", it)
			}
		default:
			t.Errorf("unexpected kind %q", it.Kind)
		}
	}
	if !sawProvider {
		t.Error("provider_name was never resolved from external_providers")
	}
	if !sawSource {
		t.Error("source_name was never resolved from data_sources")
	}
}

// TestExternalProxy_AuditStoresBodyFingerprintNotBody drives the real write
// path (Call -> CreateAuditLog) against a local upstream and asserts the audit
// row describes the request body without containing any of it. This is the
// end-to-end half of the "no plaintext values" acceptance criterion; the pure
// fingerprint math is covered in external_proxy_fingerprint_test.go.
func TestExternalProxy_AuditStoresBodyFingerprintNotBody(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	kr, err := crypto.LoadOrCreate(env.TempPath(t, "integration_secret"))
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	reg := integration.NewRegistry()
	credSvc := service.NewExternalCredentialService(env.Queries(), env.DB, kr, reg)
	provSvc := service.NewExternalProviderService(env.Queries(), env.DB)
	authz := service.NewAuthz(env.Queries(), env.DB)
	policy := service.NewCredentialUsagePolicyService(env.Queries(), env.DB, credSvc, authz)
	svc := service.NewExternalProxyService(env.Queries(), env.DB, provSvc, credSvc, authz, policy)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	svc.SetClientForTest(upstream.Client())

	ctx := context.Background()
	prov, err := provSvc.Create(ctx, "fp_probe", "Fingerprint Probe", upstream.URL)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if err := provSvc.SetWritePref(ctx, "user", env.UserA, prov.ID, true); err != nil {
		t.Fatalf("enable writes: %v", err)
	}
	if _, err := credSvc.Create(ctx, service.ExternalCredentialUpsertInput{
		OwnerType: "user",
		OwnerID:   env.UserA,
		UserID:    env.UserA,
		Provider:  integration.ProviderName("fp_probe"),
		Alias:     "default",
		RawConfig: map[string]any{"token": "tok", "auth_mode": "bearer"},
	}); err != nil {
		t.Fatalf("create credential: %v", err)
	}

	body := map[string]any{
		"title":    "board minutes",
		"password": "hunter2-do-not-log",
	}
	if _, err := svc.Call(ctx, service.ProxyInput{
		UserID:   env.UserA,
		Provider: "fp_probe",
		Method:   "POST",
		Path:     "/orders",
		Body:     body,
	}); err != nil {
		t.Fatalf("proxy call: %v", err)
	}

	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	var (
		sha, fields, method, path string
		size                      int64
	)
	if err := env.DB.QueryRow(
		`SELECT body_sha256, body_bytes, body_fields, method, path FROM external_api_audit WHERE user_id = ? ORDER BY id DESC LIMIT 1`,
		env.UserA,
	).Scan(&sha, &size, &fields, &method, &path); err != nil {
		t.Fatalf("read audit row: %v", err)
	}

	if want := fmt.Sprintf("%x", sha256.Sum256(raw)); sha != want {
		t.Errorf("body_sha256 = %q, want %q", sha, want)
	}
	if size != int64(len(raw)) {
		t.Errorf("body_bytes = %d, want %d", size, len(raw))
	}
	if fields != `["password","title"]` {
		t.Errorf("body_fields = %s, want %s", fields, `["password","title"]`)
	}
	if method != "POST" || path != "/orders" {
		t.Errorf("method/path = %s %s, want POST /orders", method, path)
	}

	// The whole persisted row must not carry a byte of the payload.
	var concatenated string
	if err := env.DB.QueryRow(
		`SELECT COALESCE(body_sha256,'') || COALESCE(body_fields,'') || COALESCE(path,'') || COALESCE(method,'')
		   FROM external_api_audit WHERE user_id = ?`,
		env.UserA,
	).Scan(&concatenated); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"hunter2-do-not-log", "board minutes"} {
		if strings.Contains(concatenated, secret) {
			t.Errorf("audit row leaked %q: %s", secret, concatenated)
		}
	}
}

// A call with no body records the empty fingerprint rather than a hash of "".
func TestExternalProxy_AuditBodylessReadHasEmptyFingerprint(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	kr, err := crypto.LoadOrCreate(env.TempPath(t, "integration_secret"))
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	reg := integration.NewRegistry()
	credSvc := service.NewExternalCredentialService(env.Queries(), env.DB, kr, reg)
	provSvc := service.NewExternalProviderService(env.Queries(), env.DB)
	authz := service.NewAuthz(env.Queries(), env.DB)
	policy := service.NewCredentialUsagePolicyService(env.Queries(), env.DB, credSvc, authz)
	svc := service.NewExternalProxyService(env.Queries(), env.DB, provSvc, credSvc, authz, policy)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()
	svc.SetClientForTest(upstream.Client())

	ctx := context.Background()
	if _, err := provSvc.Create(ctx, "fp_probe_read", "Fingerprint Probe Read", upstream.URL); err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if _, err := credSvc.Create(ctx, service.ExternalCredentialUpsertInput{
		OwnerType: "user",
		OwnerID:   env.UserA,
		UserID:    env.UserA,
		Provider:  integration.ProviderName("fp_probe_read"),
		Alias:     "default",
		RawConfig: map[string]any{"token": "tok", "auth_mode": "bearer"},
	}); err != nil {
		t.Fatalf("create credential: %v", err)
	}

	// GET is in the default safe set, so no write toggle is needed.
	if _, err := svc.Call(ctx, service.ProxyInput{
		UserID:   env.UserA,
		Provider: "fp_probe_read",
		Method:   "GET",
		Path:     "/things",
	}); err != nil {
		t.Fatalf("proxy call: %v", err)
	}

	var sha, fields string
	var size int64
	if err := env.DB.QueryRow(
		`SELECT body_sha256, body_bytes, body_fields FROM external_api_audit WHERE user_id = ? ORDER BY id DESC LIMIT 1`,
		env.UserA,
	).Scan(&sha, &size, &fields); err != nil {
		t.Fatalf("read audit row: %v", err)
	}
	if sha != "" || size != 0 || fields != "[]" {
		t.Errorf("bodyless fingerprint = (%q, %d, %q), want (\"\", 0, \"[]\")", sha, size, fields)
	}
}
