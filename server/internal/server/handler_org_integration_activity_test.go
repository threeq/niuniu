package server

import (
	"database/sql"
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	_ "modernc.org/sqlite"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// newOrgAuditTestServer wires the org audit handlers over an in-memory SQLite
// carrying the real schema. The caller id is fixed by the auth stub so each
// test can ask "what does member 8 see" vs "what does admin 7 see".
func newOrgAuditTestServer(t *testing.T, callerID int64) (*gin.Engine, *sql.DB) {
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

	srv := &Server{
		integrationAuditSvc: service.NewIntegrationAuditService(store.New(db)),
		authzSvc:            service.NewAuthz(store.New(db), db),
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("auth_user_id", callerID) })
	r.GET("/api/orgs/:id/integration-activity", srv.orgIntegrationActivity)
	r.GET("/api/orgs/:id/integration-activity/export", srv.orgIntegrationActivityExport)
	return r, db
}

// seedOrgAuditFixture mirrors the service-level fixture: one org (100) with an
// admin (7) and a plain member (8), a non-member (50), and one row of each
// kind.
func seedOrgAuditFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	stmts := []string{
		`INSERT INTO users (id, username, password_hash, display_name) VALUES
		   (7, 'ada', 'x', 'Ada Admin'), (8, 'bob', 'x', 'Bob Member'), (50, 'out', 'x', 'Out Sider')`,
		`INSERT INTO organizations (id, slug, name, created_by) VALUES (100, 'acme', 'Acme', 7)`,
		`INSERT INTO org_members (org_id, user_id, role) VALUES (100, 7, 'admin'), (100, 8, 'member')`,
		`INSERT INTO external_providers (id, name, api_base_url) VALUES (10, 'github', 'https://api.github.com')`,
		`INSERT INTO external_provider_credentials (id, owner_type, owner_id, user_id, provider, alias)
		   VALUES (30, 'org', 100, 7, 'github', 'acme-ci')`,
		`INSERT INTO external_api_audit (user_id, provider_id, credential_id, method, path, status_code, created_at)
		   VALUES (7, 10, 30, 'GET', '/a', 200, '2026-10-01 10:00:00')`,
		`INSERT INTO data_source_audit (user_id, source_id, access_mode, statement_summary, status, created_at)
		   VALUES (8, 20, 'read', 'SELECT 1', 'ok', '2026-10-02 10:00:00')`,
		`INSERT INTO org_audit_log (org_id, actor_user_id, actor_label, action, target_type, target_id, payload, created_at)
		   VALUES (100, 7, 'Ada Admin', 'integration.approval.approved', 'integration_approval', 77,
		           '{"action":"publish","subject_label":"github","requested_by":8,"summary":"publish"}', '2026-10-03 10:00:00')`,
		// A non-member's call must not appear in the org feed.
		`INSERT INTO external_api_audit (user_id, provider_id, credential_id, method, path, status_code, created_at)
		   VALUES (50, 10, 30, 'GET', '/leak', 200, '2026-10-04 10:00:00')`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
}

func doGet(r *gin.Engine, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
	return w
}

// A plain member must be refused by the API, not merely denied a button.
func TestOrgIntegrationActivity_MemberForbidden(t *testing.T) {
	r, db := newOrgAuditTestServer(t, 8)
	seedOrgAuditFixture(t, db)

	w := doGet(r, "/api/orgs/100/integration-activity")
	if w.Code != http.StatusForbidden {
		t.Fatalf("member: want 403, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "FORBIDDEN") {
		t.Errorf("member: want a FORBIDDEN envelope, got %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "/a") || strings.Contains(w.Body.String(), "Ada") {
		t.Errorf("member: the 403 body leaked audit content: %s", w.Body.String())
	}
}

func TestOrgIntegrationActivity_NonMemberForbidden(t *testing.T) {
	r, db := newOrgAuditTestServer(t, 50)
	seedOrgAuditFixture(t, db)

	if w := doGet(r, "/api/orgs/100/integration-activity"); w.Code != http.StatusForbidden {
		t.Fatalf("non-member: want 403, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOrgIntegrationActivity_AdminGetsMergedFeed(t *testing.T) {
	r, db := newOrgAuditTestServer(t, 7)
	seedOrgAuditFixture(t, db)

	w := doGet(r, "/api/orgs/100/integration-activity")
	if w.Code != http.StatusOK {
		t.Fatalf("admin: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{`"kind":"approval"`, `"kind":"data"`, `"kind":"api"`, `"user_name":"Bob Member"`} {
		if !strings.Contains(body, want) {
			t.Errorf("admin feed missing %s: %s", want, body)
		}
	}
	if strings.Contains(body, "/leak") {
		t.Errorf("admin feed leaked a non-member's call: %s", body)
	}
}

func TestOrgIntegrationActivity_UnauthenticatedAndBadFilters(t *testing.T) {
	r, db := newOrgAuditTestServer(t, 0)
	seedOrgAuditFixture(t, db)

	if w := doGet(r, "/api/orgs/100/integration-activity"); w.Code != http.StatusUnauthorized {
		t.Errorf("no caller: want 401, got %d", w.Code)
	}

	r, db = newOrgAuditTestServer(t, 7)
	seedOrgAuditFixture(t, db)
	for _, target := range []string{
		"/api/orgs/abc/integration-activity",
		"/api/orgs/100/integration-activity?kind=bogus",
		"/api/orgs/100/integration-activity?outcome=bogus",
		"/api/orgs/100/integration-activity?credential_scope=bogus",
		"/api/orgs/100/integration-activity?limit=-1",
		"/api/orgs/100/integration-activity?from=not-a-date",
	} {
		if w := doGet(r, target); w.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d body=%s", target, w.Code, w.Body.String())
		}
	}
}

func TestOrgIntegrationActivityExport_CSVAndLeavesAnAuditTrail(t *testing.T) {
	r, db := newOrgAuditTestServer(t, 7)
	seedOrgAuditFixture(t, db)

	w := doGet(r, "/api/orgs/100/integration-activity/export")
	if w.Code != http.StatusOK {
		t.Fatalf("export: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("export: want text/csv, got %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("export: want an attachment download, got %q", cd)
	}

	raw := w.Body.Bytes()
	if len(raw) < 3 || raw[0] != 0xEF || raw[1] != 0xBB || raw[2] != 0xBF {
		t.Errorf("export: missing UTF-8 BOM (Excel would mangle Chinese cells)")
	}
	records, err := csv.NewReader(strings.NewReader(string(raw[3:]))).ReadAll()
	if err != nil {
		t.Fatalf("export is not valid CSV: %v", err)
	}
	if len(records) != 4 { // header + api + data + approval
		t.Fatalf("export: want header + 3 rows, got %d records: %v", len(records), records)
	}
	if records[0][0] != "created_at" || records[0][1] != "kind" {
		t.Errorf("export: unexpected header %v", records[0])
	}
	if strings.Contains(string(raw), "/leak") {
		t.Errorf("export leaked a non-member's call")
	}

	// Reading the compliance record is itself worth recording.
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM org_audit_log WHERE action = 'integration.audit.exported' AND org_id = 100`,
	).Scan(&n); err != nil {
		t.Fatalf("query export audit: %v", err)
	}
	if n != 1 {
		t.Errorf("export audit trail: want 1 entry, got %d", n)
	}
}

func TestOrgIntegrationActivityExport_MemberForbidden(t *testing.T) {
	r, db := newOrgAuditTestServer(t, 8)
	seedOrgAuditFixture(t, db)

	if w := doGet(r, "/api/orgs/100/integration-activity/export"); w.Code != http.StatusForbidden {
		t.Fatalf("member export: want 403, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestAuditViewHasNoMutatingRoute is half of the append-only guarantee (#743
// acceptance: "审计记录不可改删（API 无入口 + 测试）"). It reads the real route
// table source, so adding a POST/PUT/PATCH/DELETE for either audit view fails
// here rather than reaching production.
func TestAuditViewHasNoMutatingRoute(t *testing.T) {
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("read router.go: %v", err)
	}
	checked := 0
	for i, line := range strings.Split(string(src), "\n") {
		if !strings.Contains(line, "integration-activity") {
			continue
		}
		trimmed := strings.TrimSpace(line)
		// Registration lines look like `orgs.GET("/...", handler)`; anything
		// else on a matching line is a comment or string mention.
		dot := strings.Index(trimmed, ".")
		open := strings.Index(trimmed, "(")
		if dot < 0 || open < dot {
			continue
		}
		checked++
		if method := trimmed[dot+1 : open]; method != "GET" {
			t.Errorf("router.go:%d registers %s for an audit view (must be read-only): %s", i+1, method, trimmed)
		}
	}
	if checked < 2 {
		t.Fatalf("expected at least the personal and org audit routes, matched %d", checked)
	}
}

// The export is opened in a spreadsheet by the auditor; the values in it were
// chosen by the people being audited. A cell starting with =, +, -, @, TAB or
// CR would be evaluated as a formula in Excel / Sheets / LibreOffice, letting
// an audited member plant code that runs on the auditor's machine.
func TestCsvExport_NeutralisesSpreadsheetFormulaInjection(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{`=HYPERLINK("http://evil/","x")`, `'=HYPERLINK("http://evil/","x")`},
		{"+SUM(1)", "'+SUM(1)"},
		{"-2+3", "'-2+3"},
		{"@import", "'@import"},
		{"\tcmd", "'\tcmd"},
		{"\r=1", "'\r=1"},
		{" =1", "' =1"},
		{"SELECT 1", "SELECT 1"},
		{"2026-10-01T10:00:00Z", "2026-10-01T10:00:00Z"},
		{"", ""},
	} {
		if got := csvSafeCell(tc.in); got != tc.want {
			t.Errorf("csvSafeCell(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// A whole row is sanitised, not just the fields we remembered.
	row := csvRow(service.OrgActivityItem{
		Kind:             service.ActivityKindData,
		UserName:         "@import",
		SourceName:       "+SUM(1)",
		StatementSummary: `=HYPERLINK("http://evil/","x")`,
	})
	for i, cell := range row {
		if cell == "" {
			continue
		}
		if strings.ContainsRune("=+-@", rune(cell[0])) {
			t.Errorf("export column %d (%q) still starts with a formula trigger", i, cell)
		}
	}
	if row[9] != `'=HYPERLINK("http://evil/","x")` {
		t.Errorf("statement summary not neutralised: %q", row[9])
	}
}
