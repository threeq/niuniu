package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	_ "modernc.org/sqlite"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// newAuditTestServer wires the audit handler over an in-memory SQLite carrying
// the real schema, behind an auth stub that pins the caller to user 1.
func newAuditTestServer(t *testing.T) (*gin.Engine, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store.Driver = "sqlite"
	if err := store.ApplySchema(db); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	store.Migrate(db)

	srv := &Server{integrationAuditSvc: service.NewIntegrationAuditService(store.New(db))}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("auth_user_id", int64(1)) })
	r.GET("/api/me/integration-activity", srv.integrationActivity)
	r.GET("/api/me/external-proxy/audit", srv.proxyAudit)
	r.GET("/api/me/data-sources/audit", srv.dataSourceAudit)
	return r, db
}

// seedAuditTimeline writes an interleaved 3-api / 2-data timeline for user 1.
func seedAuditTimeline(t *testing.T, db *sql.DB) {
	t.Helper()
	stmts := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO external_api_audit (user_id, provider_id, method, path, status_code, created_at) VALUES (1, 10, 'GET', '/a', 200, '2026-10-01 10:00:00')`, nil},
		{`INSERT INTO data_source_audit (user_id, source_id, access_mode, statement_summary, status, created_at) VALUES (1, 20, 'read', 'SELECT 1', 'ok', '2026-10-02 10:00:00')`, nil},
		{`INSERT INTO external_api_audit (user_id, provider_id, method, path, status_code, created_at) VALUES (1, 10, 'POST', '/b', 201, '2026-10-03 10:00:00')`, nil},
		{`INSERT INTO data_source_audit (user_id, source_id, access_mode, statement_summary, status, created_at) VALUES (1, 21, 'read', 'SELECT 2', 'ok', '2026-10-04 10:00:00')`, nil},
		{`INSERT INTO external_api_audit (user_id, provider_id, method, path, status_code, created_at) VALUES (1, 11, 'DELETE', '/c', 204, '2026-10-05 10:00:00')`, nil},
	}
	for _, s := range stmts {
		if _, err := db.Exec(s.q, s.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

type activityPageResp struct {
	Data struct {
		Items []struct {
			Kind             string `json:"kind"`
			Path             string `json:"path"`
			StatementSummary string `json:"statement_summary"`
		} `json:"items"`
		Total  int64 `json:"total"`
		Limit  int64 `json:"limit"`
		Offset int64 `json:"offset"`
	} `json:"data"`
}

func getActivity(t *testing.T, r *gin.Engine, url string) (*httptest.ResponseRecorder, activityPageResp) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	r.ServeHTTP(w, req)
	var body activityPageResp
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %s: %v (%s)", url, err, w.Body.String())
		}
	}
	return w, body
}

func labels(items []struct {
	Kind             string `json:"kind"`
	Path             string `json:"path"`
	StatementSummary string `json:"statement_summary"`
}) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		if it.Kind == service.ActivityKindAPI {
			out = append(out, "api:"+it.Path)
		} else {
			out = append(out, "data:"+it.StatementSummary)
		}
	}
	return out
}

func TestIntegrationActivityEndpoint_Pagination(t *testing.T) {
	r, db := newAuditTestServer(t)
	seedAuditTimeline(t, db)

	w, page := getActivity(t, r, "/api/me/integration-activity?limit=2&offset=0")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if fmt.Sprint(labels(page.Data.Items)) != fmt.Sprint([]string{"api:/c", "data:SELECT 2"}) {
		t.Errorf("page 1 = %v", labels(page.Data.Items))
	}
	if page.Data.Total != 5 || page.Data.Limit != 2 || page.Data.Offset != 0 {
		t.Errorf("page meta = total %d limit %d offset %d, want 5/2/0", page.Data.Total, page.Data.Limit, page.Data.Offset)
	}

	_, page = getActivity(t, r, "/api/me/integration-activity?limit=2&offset=2")
	if fmt.Sprint(labels(page.Data.Items)) != fmt.Sprint([]string{"api:/b", "data:SELECT 1"}) {
		t.Errorf("page 2 = %v", labels(page.Data.Items))
	}
	_, page = getActivity(t, r, "/api/me/integration-activity?limit=2&offset=4")
	if fmt.Sprint(labels(page.Data.Items)) != fmt.Sprint([]string{"api:/a"}) {
		t.Errorf("page 3 = %v", labels(page.Data.Items))
	}
}

func TestIntegrationActivityEndpoint_Filters(t *testing.T) {
	r, db := newAuditTestServer(t)
	seedAuditTimeline(t, db)

	cases := []struct {
		url  string
		want []string
	}{
		{"/api/me/integration-activity?kind=api", []string{"api:/c", "api:/b", "api:/a"}},
		{"/api/me/integration-activity?kind=data", []string{"data:SELECT 2", "data:SELECT 1"}},
		{"/api/me/integration-activity?provider_id=10", []string{"api:/b", "api:/a"}},
		{"/api/me/integration-activity?source_id=21", []string{"data:SELECT 2"}},
		// Date-only bounds cover the whole named day.
		{"/api/me/integration-activity?from=2026-10-03&to=2026-10-04", []string{"data:SELECT 2", "api:/b"}},
		{"/api/me/integration-activity?from=2026-10-05T00:00:00Z", []string{"api:/c"}},
	}
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			w, page := getActivity(t, r, tc.url)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
			}
			if got := labels(page.Data.Items); fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("items = %v, want %v", got, tc.want)
			}
			if page.Data.Total != int64(len(tc.want)) {
				t.Errorf("total = %d, want %d", page.Data.Total, len(tc.want))
			}
		})
	}
}

// The two prefix-scoped routes must pin their kind even when the caller asks
// for the other one.
func TestIntegrationActivityEndpoint_PrefixRoutesPinKind(t *testing.T) {
	r, db := newAuditTestServer(t)
	seedAuditTimeline(t, db)

	_, page := getActivity(t, r, "/api/me/external-proxy/audit")
	if got := labels(page.Data.Items); fmt.Sprint(got) != fmt.Sprint([]string{"api:/c", "api:/b", "api:/a"}) {
		t.Errorf("external-proxy/audit = %v", got)
	}
	_, page = getActivity(t, r, "/api/me/external-proxy/audit?kind=data")
	if got := labels(page.Data.Items); fmt.Sprint(got) != fmt.Sprint([]string{"api:/c", "api:/b", "api:/a"}) {
		t.Errorf("external-proxy/audit?kind=data = %v (kind must stay pinned)", got)
	}

	_, page = getActivity(t, r, "/api/me/data-sources/audit")
	if got := labels(page.Data.Items); fmt.Sprint(got) != fmt.Sprint([]string{"data:SELECT 2", "data:SELECT 1"}) {
		t.Errorf("data-sources/audit = %v", got)
	}
}

func TestIntegrationActivityEndpoint_BadInput(t *testing.T) {
	r, db := newAuditTestServer(t)
	seedAuditTimeline(t, db)

	for _, url := range []string{
		"/api/me/integration-activity?kind=bogus",
		"/api/me/integration-activity?limit=-1",
		"/api/me/integration-activity?offset=abc",
		"/api/me/integration-activity?from=not-a-date",
		"/api/me/integration-activity?to=2026-13-45",
	} {
		w, _ := getActivity(t, r, url)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", url, w.Code, w.Body.String())
		}
	}
}

func TestIntegrationActivityEndpoint_RequiresIdentity(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store.Driver = "sqlite"
	if err := store.ApplySchema(db); err != nil {
		t.Fatal(err)
	}
	srv := &Server{integrationAuditSvc: service.NewIntegrationAuditService(store.New(db))}

	gin.SetMode(gin.TestMode)
	r := gin.New() // no auth stub -> auth_user_id is unset
	r.GET("/api/me/integration-activity", srv.integrationActivity)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/me/integration-activity", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}
