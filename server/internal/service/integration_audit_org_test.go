// Tests for the team-edition cross-user audit view (#743): org-scoped
// aggregation over external_api_audit + data_source_audit + the approval
// decisions #741 wrote to org_audit_log, plus the admin gate.
package service_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/service"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
)

const (
	orgAcme      int64 = 100
	orgOther     int64 = 101
	uidAdmin     int64 = 7
	uidMember    int64 = 8
	uidOwner     int64 = 9
	uidOutsider  int64 = 50
	credOrg      int64 = 30
	credPersonal int64 = 31
	credOutsider int64 = 32
	provGithub   int64 = 10
	srcOrg       int64 = 20
	srcPersonal  int64 = 21
)

// orgAuditFixture builds one org (acme) with three members of differing roles,
// a second org, and a non-member — then an interleaved timeline across all
// three sources:
//
//	2026-10-01  api       admin    org credential      GET  /a     200
//	2026-10-02  data      member   org data source     ok
//	2026-10-03  approval  owner    (decided approved)
//	2026-10-04  api       member   personal credential POST /b     500
//	2026-10-05  data      member   personal source     ok
//	2026-10-06  api       outsider org credential      GET  /x     200   <- excluded
//	2026-10-07  org_audit member.added (not an approval)                 <- excluded
//	2026-10-08  api       acme?    other org's row                        <- excluded
func orgAuditFixture(t *testing.T) (*sql.DB, *service.IntegrationAuditService, *service.Authz) {
	t.Helper()
	db, q := niutest.SetupDBRaw(t)

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}

	for _, u := range []struct {
		id, role, name string
	}{
		{"7", "admin", "Ada Admin"},
		{"8", "member", "Bob Member"},
		{"9", "member", "Ola Owner"},
		{"50", "member", "Out Sider"},
	} {
		exec(`INSERT INTO users (id, username, password_hash, display_name, role) VALUES (?, ?, 'x', ?, ?)`,
			u.id, "user"+u.id, u.name, u.role)
	}
	exec(`INSERT INTO organizations (id, slug, name, created_by) VALUES (100, 'acme', 'Acme', 7), (101, 'other', 'Other', 7)`)
	exec(`INSERT INTO org_members (org_id, user_id, role) VALUES (100, 7, 'admin'), (100, 8, 'member'), (100, 9, 'owner'), (101, 50, 'member')`)

	exec(`INSERT INTO external_providers (id, name, api_base_url) VALUES (10, 'github', 'https://api.github.com')`)
	exec(`INSERT INTO external_provider_credentials (id, owner_type, owner_id, user_id, provider, alias)
	      VALUES (30, 'org', 100, 7, 'github', 'acme-ci'),
	             (31, 'user', 7, 8, 'github', 'bob-personal'),
	             (32, 'org', 100, 50, 'github', 'outsider-org')`)

	exec(`INSERT INTO data_sources (id, owner_type, owner_id, user_id, name, kind) VALUES
	        (20, 'org', 100, 7, 'acme-warehouse', 'postgres'),
	        (21, 'user', 8, 8, 'bob-scratch', 'postgres')`)

	// The project dimension: one project of the org, with the org credential
	// and the org data source bound to it. "What did this project's agents do"
	// resolves through these bindings.
	exec(`INSERT INTO projects (id, owner_type, owner_id, name) VALUES (40, 'org', 100, 'acme-project')`)
	exec(`INSERT INTO project_external_sources (project_id, provider, source_key, credential_id)
	      VALUES (40, 'github', 'acme', 30)`)
	exec(`INSERT INTO data_source_bindings (source_id, target_type, target_id) VALUES (20, 'project', 40)`)
	// The approval audit row above targets request 77; the request carries the
	// project (the audit row itself has no project column).
	exec(`INSERT INTO integration_approval_requests
	        (id, owner_type, owner_id, project_id, action, subject_type, subject_id,
	         subject_label, requested_by, required_role, status, expires_at)
	      VALUES (77, 'org', 100, 40, 'draft_confirm', 'provider', 10,
	              'github', 8, 'project_admin', 'approved', '2026-10-10 10:00:00')`)

	// Interleaved timeline. created_at is explicit so merge order, paging and
	// the time filters are all deterministic.
	exec(`INSERT INTO external_api_audit (user_id, provider_id, credential_id, method, path, status_code, body_sha256, body_bytes, body_fields, created_at)
	      VALUES (7, 10, 30, 'GET', '/a', 200, '', 0, '[]', '2026-10-01 10:00:00')`)
	exec(`INSERT INTO data_source_audit (user_id, source_id, access_mode, database_name, objects, statement_summary, status, "rows", duration_ms, created_at)
	      VALUES (8, 20, 'read', 'app', '[]', 'SELECT 1', 'ok', 3, 12, '2026-10-02 10:00:00')`)
	exec(`INSERT INTO org_audit_log (org_id, actor_user_id, actor_label, action, target_type, target_id, payload, created_at)
	      VALUES (100, 9, 'Ola Owner', 'integration.approval.approved', 'integration_approval', 77,
	              '{"action":"draft_confirm","subject_type":"provider","subject_id":10,"subject_label":"github","requested_by":8,"summary":"放宽 scope","diff":"- scope: read","note":"ok"}',
	              '2026-10-03 10:00:00')`)
	exec(`INSERT INTO external_api_audit (user_id, provider_id, credential_id, method, path, status_code, body_sha256, body_bytes, body_fields, created_at)
	      VALUES (8, 10, 31, 'POST', '/b', 500, 'deadbeef', 12, '["name"]', '2026-10-04 10:00:00')`)
	exec(`INSERT INTO data_source_audit (user_id, source_id, access_mode, database_name, objects, statement_summary, status, "rows", duration_ms, created_at)
	      VALUES (8, 21, 'read', 'app', '[]', 'SELECT 2', 'ok', 1, 5, '2026-10-05 10:00:00')`)

	// Excluded: a non-member's call, an org audit row that is not an approval,
	// and another org's approval decision.
	exec(`INSERT INTO external_api_audit (user_id, provider_id, credential_id, method, path, status_code, created_at)
	      VALUES (50, 10, 32, 'GET', '/x', 200, '2026-10-06 10:00:00')`)
	exec(`INSERT INTO org_audit_log (org_id, actor_user_id, actor_label, action, target_type, target_id, payload, created_at)
	      VALUES (100, 7, 'Ada Admin', 'member.added', 'member', 8, '{}', '2026-10-07 10:00:00')`)
	exec(`INSERT INTO org_audit_log (org_id, actor_user_id, actor_label, action, target_type, target_id, payload, created_at)
	      VALUES (101, 50, 'Out Sider', 'integration.approval.denied', 'integration_approval', 88,
	              '{"action":"publish","subject_label":"other-org"}', '2026-10-08 10:00:00')`)

	return db, service.NewIntegrationAuditService(q), service.NewAuthz(q, db)
}

func listOrg(t *testing.T, svc *service.IntegrationAuditService, in service.OrgActivityInput) *service.OrgActivityPage {
	t.Helper()
	in.OrgID = orgAcme
	page, err := svc.ListOrgActivity(context.Background(), in)
	if err != nil {
		t.Fatalf("ListOrgActivity: %v", err)
	}
	return page
}

func TestOrgActivity_MergesMembersAcrossAllThreeSources(t *testing.T) {
	_, svc, _ := orgAuditFixture(t)

	page := listOrg(t, svc, service.OrgActivityInput{})
	if page.Total != 5 {
		t.Fatalf("total: want 5 (a non-member's call, a non-approval org audit row and another org's row must not count), got %d", page.Total)
	}
	want := []string{
		service.ActivityKindData,     // 10-05
		service.ActivityKindAPI,      // 10-04
		service.ActivityKindApproval, // 10-03
		service.ActivityKindData,     // 10-02
		service.ActivityKindAPI,      // 10-01
	}
	if len(page.Items) != len(want) {
		t.Fatalf("items: want %d, got %d", len(want), len(page.Items))
	}
	for i, k := range want {
		if page.Items[i].Kind != k {
			t.Fatalf("item %d: want kind %s, got %s (%s)", i, k, page.Items[i].Kind, page.Items[i].CreatedAt)
		}
	}
	for _, it := range page.Items {
		if it.UserID == uidOutsider {
			t.Fatalf("a non-member's activity leaked into the org feed: %+v", it)
		}
		if it.Kind == service.ActivityKindApproval && it.Action == "member.added" {
			t.Fatalf("a non-approval org audit row leaked into the feed")
		}
	}
}

func TestOrgActivity_KindFilter(t *testing.T) {
	_, svc, _ := orgAuditFixture(t)

	for _, tc := range []struct {
		kind string
		want int64
	}{
		{service.ActivityKindAPI, 2},
		{service.ActivityKindData, 2},
		{service.ActivityKindApproval, 1},
	} {
		page := listOrg(t, svc, service.OrgActivityInput{Kind: tc.kind})
		if page.Total != tc.want {
			t.Errorf("kind=%s: want %d, got %d", tc.kind, tc.want, page.Total)
		}
		for _, it := range page.Items {
			if it.Kind != tc.kind {
				t.Errorf("kind=%s: feed returned a %s row", tc.kind, it.Kind)
			}
		}
	}

	if _, err := svc.ListOrgActivity(context.Background(), service.OrgActivityInput{OrgID: orgAcme, Kind: "bogus"}); !errors.Is(err, service.ErrBadActivityKind) {
		t.Errorf("unknown kind: want ErrBadActivityKind, got %v", err)
	}
}

func TestOrgActivity_MemberAndCredentialScopeFilters(t *testing.T) {
	_, svc, _ := orgAuditFixture(t)

	if p := listOrg(t, svc, service.OrgActivityInput{MemberID: uidAdmin}); p.Total != 1 || p.Items[0].UserID != uidAdmin {
		t.Errorf("member_id=admin: want the admin's single api call, got total=%d", p.Total)
	}

	// credential_scope=org answers "which of OUR credentials was used": the
	// admin's call through the org credential plus the query against the org's
	// data source. The member's personal-credential call and personal source
	// must be excluded.
	orgScope := listOrg(t, svc, service.OrgActivityInput{CredentialScope: service.ActivityScopeOrg})
	if orgScope.Total != 2 {
		t.Fatalf("credential_scope=org: want 2, got %d", orgScope.Total)
	}
	for _, it := range orgScope.Items {
		owner := it.CredentialOwnerType
		if it.Kind == service.ActivityKindData {
			owner = it.SourceOwnerType
		}
		if owner != "org" {
			t.Errorf("credential_scope=org returned a %s row owned by %q", it.Kind, owner)
		}
	}

	personal := listOrg(t, svc, service.OrgActivityInput{CredentialScope: service.ActivityScopePersonal})
	if personal.Total != 2 {
		t.Fatalf("credential_scope=user: want 2, got %d", personal.Total)
	}
	for _, it := range personal.Items {
		owner := it.CredentialOwnerType
		if it.Kind == service.ActivityKindData {
			owner = it.SourceOwnerType
		}
		if owner != "user" {
			t.Errorf("credential_scope=user returned a %s row owned by %q", it.Kind, owner)
		}
	}

	if _, err := svc.ListOrgActivity(context.Background(), service.OrgActivityInput{OrgID: orgAcme, CredentialScope: "bogus"}); !errors.Is(err, service.ErrBadCredentialScope) {
		t.Errorf("unknown scope: want ErrBadCredentialScope, got %v", err)
	}
}

func TestOrgActivity_OutcomeFilter(t *testing.T) {
	_, svc, _ := orgAuditFixture(t)

	ok := listOrg(t, svc, service.OrgActivityInput{Outcome: service.ActivityOutcomeOK})
	if ok.Total != 4 {
		t.Errorf("outcome=ok: want 4 (two successful queries, one 200, one approval), got %d", ok.Total)
	}
	errPage := listOrg(t, svc, service.OrgActivityInput{Outcome: service.ActivityOutcomeError})
	if errPage.Total != 1 || errPage.Items[0].StatusCode == nil || *errPage.Items[0].StatusCode != 500 {
		t.Errorf("outcome=error: want the single 500, got total=%d", errPage.Total)
	}
	if p := listOrg(t, svc, service.OrgActivityInput{Outcome: service.ActivityOutcomePending}); p.Total != 0 {
		t.Errorf("outcome=pending: want 0 (nothing escalated), got %d", p.Total)
	}
	if _, err := svc.ListOrgActivity(context.Background(), service.OrgActivityInput{OrgID: orgAcme, Outcome: "bogus"}); !errors.Is(err, service.ErrBadActivityOutcome) {
		t.Errorf("unknown outcome: want ErrBadActivityOutcome, got %v", err)
	}
}

func TestOrgActivity_TimeFilterAndProviderSourceImplyKind(t *testing.T) {
	_, svc, _ := orgAuditFixture(t)

	from := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 4, 23, 59, 59, 0, time.UTC)
	p := listOrg(t, svc, service.OrgActivityInput{From: from, To: to})
	if p.Total != 2 {
		t.Errorf("time window: want 2 (the approval and the 500), got %d", p.Total)
	}

	// A provider/source id can only ever match its own side, so it must not
	// force the other kind back on -- even against an explicit kind.
	if p := listOrg(t, svc, service.OrgActivityInput{ProviderID: provGithub}); p.Total != 2 {
		t.Errorf("provider_id: want the 2 api rows, got %d", p.Total)
	}
	if p := listOrg(t, svc, service.OrgActivityInput{SourceID: srcOrg}); p.Total != 1 {
		t.Errorf("source_id: want the single org-source query, got %d", p.Total)
	}
	if p := listOrg(t, svc, service.OrgActivityInput{Kind: service.ActivityKindData, ProviderID: provGithub}); p.Total != 0 {
		t.Errorf("kind=data plus a provider filter: want empty, got %d", p.Total)
	}
}

func TestOrgActivity_Pagination(t *testing.T) {
	_, svc, _ := orgAuditFixture(t)

	seen := map[string]bool{}
	for offset := int64(0); offset < 5; offset += 2 {
		page := listOrg(t, svc, service.OrgActivityInput{Limit: 2, Offset: offset})
		if page.Total != 5 {
			t.Fatalf("offset %d: total must stay 5, got %d", offset, page.Total)
		}
		for _, it := range page.Items {
			key := it.Kind + ":" + it.CreatedAt.UTC().Format(time.RFC3339)
			if seen[key] {
				t.Errorf("offset %d: %s appeared on two pages", offset, key)
			}
			seen[key] = true
		}
	}
	if len(seen) != 5 {
		t.Errorf("paging dropped rows: saw %d of 5", len(seen))
	}
}

func TestOrgActivity_EnrichesPeopleSourcesAndCredentials(t *testing.T) {
	_, svc, _ := orgAuditFixture(t)

	page := listOrg(t, svc, service.OrgActivityInput{Limit: 50})
	byKind := map[string]service.OrgActivityItem{}
	for _, it := range page.Items {
		byKind[it.Kind+"@"+it.CreatedAt.UTC().Format("01-02")] = it
	}

	api := byKind["api@10-01"]
	if api.UserName != "Ada Admin" || api.ProviderName != "github" || api.CredentialAlias != "acme-ci" || api.CredentialOwnerType != "org" {
		t.Errorf("api row attribution incomplete: %+v", api)
	}
	data := byKind["data@10-02"]
	if data.UserName != "Bob Member" || data.SourceName != "acme-warehouse" || data.SourceOwnerType != "org" {
		t.Errorf("data row attribution incomplete: %+v", data)
	}
	appr := byKind["approval@10-03"]
	if appr.UserName != "Ola Owner" || appr.Status != "approved" || appr.Outcome != service.ActivityOutcomeOK {
		t.Errorf("approval row not decoded: %+v", appr)
	}
	if appr.RequestedBy != uidMember || appr.SubjectLabel != "github" || appr.Diff == "" {
		t.Errorf("approval payload not surfaced (who asked, what, which diff): %+v", appr)
	}
}

func TestOrgActivityExport_StreamsEveryFilteredRow(t *testing.T) {
	_, svc, _ := orgAuditFixture(t)

	var got []service.OrgActivityItem
	err := svc.ExportOrgActivity(context.Background(), service.OrgActivityInput{OrgID: orgAcme}, func(it service.OrgActivityItem) error {
		got = append(got, it)
		return nil
	})
	if err != nil {
		t.Fatalf("ExportOrgActivity: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("export: want all 5 matching rows (not just one page), got %d", len(got))
	}
	// Export must honour the filters, not widen to everything.
	got = nil
	if err := svc.ExportOrgActivity(context.Background(), service.OrgActivityInput{OrgID: orgAcme, Kind: service.ActivityKindAPI}, func(it service.OrgActivityItem) error {
		got = append(got, it)
		return nil
	}); err != nil {
		t.Fatalf("filtered export: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("filtered export: want 2 api rows, got %d", len(got))
	}
}

func TestOrgActivity_CountsMatchTheFeed(t *testing.T) {
	_, svc, _ := orgAuditFixture(t)

	counts, err := svc.OrgActivityCounts(context.Background(), service.OrgActivityInput{OrgID: orgAcme})
	if err != nil {
		t.Fatalf("OrgActivityCounts: %v", err)
	}
	want := map[string]int64{service.ActivityKindAPI: 2, service.ActivityKindData: 2, service.ActivityKindApproval: 1}
	for k, n := range want {
		if counts[k] != n {
			t.Errorf("counts[%s]: want %d, got %d", k, n, counts[k])
		}
	}
}

func TestEnsureOrgAuditReadable_OnlyOrgAdmins(t *testing.T) {
	_, _, authz := orgAuditFixture(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name   string
		userID int64
		orgID  int64
		want   bool
	}{
		{"org admin", uidAdmin, orgAcme, true},
		{"org owner", uidOwner, orgAcme, true},
		{"plain member", uidMember, orgAcme, false},
		{"non-member", uidOutsider, orgAcme, false},
		{"member of another org", uidOutsider, orgOther, false},
	} {
		err := authz.EnsureOrgAuditReadable(ctx, tc.userID, tc.orgID)
		if tc.want && err != nil {
			t.Errorf("%s: want allowed, got %v", tc.name, err)
		}
		if !tc.want && !errors.Is(err, service.ErrForbidden) {
			t.Errorf("%s: want ErrForbidden, got %v", tc.name, err)
		}
	}

	// A global admin (users.role='admin') may read every org without being a
	// member — that is how a deployment-wide auditor works.
	if err := authz.EnsureOrgAuditReadable(ctx, uidAdmin, orgOther); err != nil {
		t.Errorf("global admin over a foreign org: want allowed, got %v", err)
	}
	// ...but a member of ANOTHER org is not a global admin.
	if err := authz.EnsureOrgAuditReadable(ctx, uidMember, orgOther); !errors.Is(err, service.ErrForbidden) {
		t.Errorf("cross-org member: want ErrForbidden, got %v", err)
	}
}

func TestOrgActivityExport_RefusesOverCapRange(t *testing.T) {
	_, svc, _ := orgAuditFixture(t)

	orig := service.MaxExportRows
	// The fixture holds two api rows and two data rows, so a cap of 1 is
	// exceeded by the api side -- the cap is per source table.
	service.MaxExportRows = 1
	t.Cleanup(func() { service.MaxExportRows = orig })

	err := svc.ExportOrgActivity(context.Background(), service.OrgActivityInput{OrgID: orgAcme}, func(service.OrgActivityItem) error { return nil })
	if !errors.Is(err, service.ErrExportTooLarge) {
		t.Fatalf("over-cap export: want ErrExportTooLarge (a truncated compliance export is worse than none), got %v", err)
	}
}

// Guard against the fixture drifting from the migration: the credential
// attribution column the org view scopes on must round-trip.
func TestOrgActivity_CredentialAttributionRoundTrips(t *testing.T) {
	db, svc, _ := orgAuditFixture(t)

	var credID int64
	if err := db.QueryRow(`SELECT credential_id FROM external_api_audit WHERE path = '/a'`).Scan(&credID); err != nil {
		t.Fatalf("read credential_id: %v", err)
	}
	if credID != credOrg {
		t.Fatalf("credential_id: want %d, got %d", credOrg, credID)
	}

	page := listOrg(t, svc, service.OrgActivityInput{Kind: service.ActivityKindAPI, CredentialScope: service.ActivityScopeOrg})
	if page.Total != 1 || page.Items[0].CredentialID != credOrg {
		t.Fatalf("org-scoped api feed: want the credential-%d row, got %+v", credOrg, page.Items)
	}
}

func TestOrgActivity_ProjectFilter(t *testing.T) {
	_, svc, _ := orgAuditFixture(t)

	// The project is bound to the org credential and the org data source, and
	// the approval decision belongs to a request made for that project: all
	// three kinds must resolve it.
	scoped := listOrg(t, svc, service.OrgActivityInput{ProjectID: 40})
	if scoped.Total != 3 {
		t.Fatalf("project filter: want the 3 bound rows (api + data + approval), got %d", scoped.Total)
	}
	kinds := map[string]bool{}
	for _, it := range scoped.Items {
		kinds[it.Kind] = true
	}
	for _, k := range []string{service.ActivityKindAPI, service.ActivityKindData, service.ActivityKindApproval} {
		if !kinds[k] {
			t.Errorf("project filter dropped the %s kind", k)
		}
	}

	// The member's personal-credential call and personal source are not bound
	// to any project, so a project filter must exclude them...
	for _, it := range scoped.Items {
		if it.CredentialOwnerType == "user" || it.SourceOwnerType == "user" {
			t.Errorf("project filter leaked an unbound personal resource: %+v", it)
		}
	}
	// ...and a project with no bindings matches nothing.
	if p := listOrg(t, svc, service.OrgActivityInput{ProjectID: 999}); p.Total != 0 {
		t.Errorf("unbound project: want 0, got %d", p.Total)
	}
}
