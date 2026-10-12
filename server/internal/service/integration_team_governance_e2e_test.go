// The team-edition governance end-to-end demo (Epic #740): one story line
// across all three shipped capabilities —
//
//	organization OrgA (admin = owner UserA) + two plain members
//	(team-a drafts, team-b uses) + one org credential (github) + one org
//	data source bound to a project.
//
// The story the Epic asks for:
//
//  1. member A submits a scope_relax approval for the org credential
//  2. the admin approves it (the card routes to role holders; the requester
//     cannot self-approve)
//  3. approval alone does not open the credential (default-minimal): the
//     admin grants the project scope, then the binding succeeds
//  4. member B uses the org credential through the project binding (real
//     HTTP round trip against a local upstream)
//  5. the whole chain is auditable per person / per project: the org admin
//     sees who drafted, who approved, and who used which credential on what;
//     a plain member cannot read the org-wide view.
//
// Runs entirely on the SQLite test harness; the upstream HTTP call goes to an
// httptest server, whitelisted past the SSRF guard's loopback block.
package service_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/niuniu-dev/niuniu/internal/dataconn"
	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
)

func TestTeamGovernanceEndToEnd(t *testing.T) {
	ctx := context.Background()
	env := niutest.NewIsolationEnv(t) // OrgA owned by UserA
	admin, orgID := env.UserA, env.OrgA

	// ---- services, wired exactly like the API process -------------------
	kr, err := crypto.LoadOrCreate(env.TempPath(t, "integration_secret"))
	require.NoError(t, err)
	reg := integration.NewRegistry()
	authz := service.NewAuthz(env.Queries(), env.DB)
	orgSvc := service.NewOrgService(env.Queries(), env.DB, authz)
	creds := service.NewExternalCredentialService(env.Queries(), env.DB, kr, reg)
	policy := service.NewCredentialUsagePolicyService(env.Queries(), env.DB, creds, authz)
	provSvc := service.NewExternalProviderService(env.Queries(), env.DB)
	proxy := service.NewExternalProxyService(env.Queries(), env.DB, provSvc, creds, authz, policy)
	srcSvc := service.NewExternalSourceService(env.Queries(), env.DB, creds, reg, authz, policy)
	approvals := service.NewIntegrationApprovalService(env.Queries(), authz, nil)
	projSvc := service.NewProjectMemberService(env.Queries(), authz)
	dsSvc := service.NewDataSourceService(env.Queries(), env.DB, kr, dataconn.NewRegistry())
	auditSvc := service.NewIntegrationAuditService(env.Queries())

	// ---- the cast: two plain members inside the org ----------------------
	memberA := insertTeamUser(t, env, "team-a") // drafts
	memberB := insertTeamUser(t, env, "team-b") // uses
	require.NoError(t, orgSvc.AddMember(ctx, admin, "admin", orgID, memberA, "member"))
	require.NoError(t, orgSvc.AddMember(ctx, admin, "admin", orgID, memberB, "member"))

	// ---- the mount point: an org project + workspace ---------------------
	proj := insertOrgProject(t, env, orgID, "governance-demo")
	ws := insertOrgWorkspace(t, env, orgID, proj)
	require.NoError(t, projSvc.Grant(ctx, admin, proj, memberA))
	require.NoError(t, projSvc.Grant(ctx, admin, proj, memberB))

	// ---- the assets: one org credential + one org data source ------------
	const orgToken = "team-edition-org-token"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+orgToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"path":%q}`, r.URL.Path)
	}))
	defer upstream.Close()
	// Loopback upstream: inject the test client, the same hatch the proxy
	// fingerprint tests use, instead of dialing through the SSRF guard.
	proxy.SetClientForTest(upstream.Client())

	var provID int64
	require.NoError(t, store.Wrap(env.DB).QueryRowContext(ctx,
		`INSERT INTO external_providers (name, api_base_url) VALUES ('github', ?) RETURNING id`,
		upstream.URL).Scan(&provID))

	cred, err := creds.Create(ctx, service.ExternalCredentialUpsertInput{
		OwnerType: "org", OwnerID: orgID, UserID: admin,
		Provider:  integration.ProviderName("github"),
		Alias:     "acme-ci",
		RawConfig: map[string]any{"token": orgToken},
	})
	require.NoError(t, err)
	t.Logf("PASS 0 资产就绪：组织凭证 acme-ci(#%d, owner=org/%d) + 组织数据源 + 项目 #%d + 工作空间 #%d",
		cred.ID, orgID, proj, ws)

	dsID, err := dsSvc.Create(ctx, service.CreateDataSourceInput{
		OwnerType: "org", OwnerID: orgID, UserID: admin,
		Name: "acme-warehouse", Kind: "mysql",
		Config:      map[string]any{"host": "db.internal", "port": 3306, "user": "ro", "password": "pw", "database": "app"},
		ScopeConfig: map[string]any{"databases": []any{"app"}},
	})
	require.NoError(t, err)
	require.NoError(t, dsSvc.AddBinding(ctx, dsID, admin, []int64{orgID},
		service.DataSourceBinding{TargetType: "project", TargetID: proj}))

	// ---- 1. member A drafts: scope_relax for the credential on the project
	req, err := approvals.Submit(ctx, service.SubmitApprovalInput{
		OwnerType: "org", OwnerID: orgID, ProjectID: proj,
		Action:       service.ApprovalScopeRelax,
		SubjectType:  "credential",
		SubjectID:    cred.ID,
		SubjectLabel: "acme-ci",
		Diff:         fmt.Sprintf(`{"grant":{"scope_type":"project","scope_id":%d}}`, proj),
		Summary:      "把组织凭证 acme-ci 授权给项目 governance-demo（只读使用）",
		RequestedBy:  memberA,
	})
	require.NoError(t, err)
	assert.Equal(t, "pending", req.Status)
	assert.Equal(t, "admin", req.RequiredRole, "scope_relax must require the admin role")
	t.Logf("PASS 1 成员 A(team-a) 起草：审批请求 #%d action=scope_relax required_role=%s status=pending", req.ID, req.RequiredRole)

	// The card routes to role holders, never back to the requester.
	pendingAdmin, err := approvals.ListPendingForApprover(ctx, admin)
	require.NoError(t, err)
	assert.True(t, containsRequest(pendingAdmin, req.ID), "admin's pending list must contain the request")
	pendingA, err := approvals.ListPendingForApprover(ctx, memberA)
	require.NoError(t, err)
	assert.False(t, containsRequest(pendingA, req.ID), "requester must not be routed their own card")
	_, err = approvals.Decide(ctx, memberA, "team-a", req.ID, true, "")
	assert.ErrorIs(t, err, service.ErrApprovalSelfDecide)
	t.Logf("PASS 2 路由正确：待批卡在管理员的队列里；成员 A 自批被拒（SELF_DECIDE）")

	// ---- 2. the admin approves -------------------------------------------
	decided, err := approvals.Decide(ctx, admin, "OrgA Admin", req.ID, true, "批准：项目内只读使用")
	require.NoError(t, err)
	assert.Equal(t, "approved", decided.Status)
	t.Logf("PASS 3 管理员批准：request #%d -> approved（审批留痕待审计验证）", req.ID)

	// ---- 3. default-minimal: approval alone opens nothing ----------------
	_, err = srcSvc.Add(ctx, proj, integration.ProviderName("github"), "acme", cred.ID, nil, admin)
	assert.ErrorIs(t, err, service.ErrCredentialNotGranted,
		"an approved-but-ungranted org credential must still not bind")
	_, err = proxy.Call(ctx, service.ProxyInput{
		UserID: memberB, Provider: "github", Method: "GET", Path: "/user", WorkspaceID: ws,
	})
	assert.ErrorIs(t, err, service.ErrNoProjectSource,
		"nothing is bound yet, so the call must not reach any credential")
	t.Logf("PASS 4 默认最小：批准 ≠ 授权——未 grant 前绑定被拒（credential_not_granted）")

	// ---- 4. the admin grants the project scope, then the binding lands ---
	require.NoError(t, policy.Grant(ctx, admin, cred.ID, service.GrantScopeProject, proj))
	_, err = srcSvc.Add(ctx, proj, integration.ProviderName("github"), "acme", cred.ID, nil, admin)
	require.NoError(t, err, "granted org credential must bind")
	t.Logf("PASS 5 授权生效：管理员 grant 项目 scope 后，绑定成功")

	// ---- 5. member B uses the org credential through the binding ---------
	out, err := proxy.Call(ctx, service.ProxyInput{
		UserID: memberB, Provider: "github", Method: "GET", Path: "/user", WorkspaceID: ws,
	})
	require.NoError(t, err)
	assert.Equal(t, 200, out.Status)
	t.Logf("PASS 6 成员 B(team-b) 使用组织凭证：GET /user -> 200（经项目绑定，凭证对成员不可见）")

	// ---- 6. the org admin's cross-user audit view answers who/when/what --
	page, err := auditSvc.ListOrgActivity(ctx, service.OrgActivityInput{OrgID: orgID})
	require.NoError(t, err)

	var apiRow *service.OrgActivityItem
	var approvalRows int
	for i := range page.Items {
		it := &page.Items[i]
		if it.Kind == "api" && it.UserID == memberB {
			apiRow = it
		}
		if it.Kind == "approval" {
			approvalRows++
		}
	}
	require.NotNil(t, apiRow, "audit feed must contain member B's org-credential call")
	assert.Equal(t, "org", apiRow.CredentialOwnerType, "the call must be attributed to the ORG credential")
	assert.Equal(t, cred.ID, apiRow.CredentialID)
	assert.Equal(t, "github", apiRow.ProviderName)
	assert.Equal(t, "GET", apiRow.Method)
	assert.NotNil(t, apiRow.StatusCode)
	assert.Equal(t, int64(200), *apiRow.StatusCode)
	assert.GreaterOrEqual(t, approvalRows, 1, "the draft/approve decisions must appear as approval rows")
	t.Logf("PASS 7 审计（管理员视角）：api 行 user=B credential=acme-ci(org) GET /user 200；approval 行 %d 条", approvalRows)

	// Filters: per member, per project.
	byMember, err := auditSvc.ListOrgActivity(ctx, service.OrgActivityInput{OrgID: orgID, MemberID: memberB, Kind: "api"})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(byMember.Items), 1)
	for _, it := range byMember.Items {
		assert.Equal(t, memberB, it.UserID)
	}
	byProject, err := auditSvc.ListOrgActivity(ctx, service.OrgActivityInput{OrgID: orgID, ProjectID: proj})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, byProject.Total, int64(1), "project filter must still see the activity")
	t.Logf("PASS 8 过滤：按人(team-b)与按项目(#%d)均可查", proj)

	// The gate: admins read, plain members do not.
	assert.NoError(t, authz.EnsureOrgAuditReadable(ctx, admin, orgID))
	assert.Error(t, authz.EnsureOrgAuditReadable(ctx, memberA, orgID),
		"a plain member must not read the cross-user audit view")
	t.Logf("PASS 9 权限：管理员可读跨用户审计；普通成员被 API 拒绝")

	// Raw trail: usage + call rows carry the right actor and credential.
	var used, audited int
	require.NoError(t, store.Wrap(env.DB).QueryRowContext(ctx,
		`SELECT COUNT(*) FROM org_audit_log WHERE org_id = ? AND action = 'integration.credential.used' AND actor_user_id = ?`,
		orgID, memberB).Scan(&used))
	assert.GreaterOrEqual(t, used, 1)
	require.NoError(t, store.Wrap(env.DB).QueryRowContext(ctx,
		`SELECT COUNT(*) FROM external_api_audit WHERE user_id = ? AND credential_id = ? AND status_code = 200`,
		memberB, cred.ID).Scan(&audited))
	assert.GreaterOrEqual(t, audited, 1)
	t.Logf("PASS 10 原始留痕：org_audit_log(used by team-b)=%d, external_api_audit(user=team-b,cred=acme-ci,200)=%d", used, audited)

	t.Log("==> TEAM GOVERNANCE E2E: ALL PASS（A 起草 -> 管理员批准 -> B 使用 -> 按人/项目可审计）")
}

func insertTeamUser(t *testing.T, env *niutest.IsolationEnv, username string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, store.Wrap(env.DB).QueryRowContext(context.Background(),
		`INSERT INTO users (username, password_hash, role) VALUES (?, 'x', 'member') RETURNING id`,
		username).Scan(&id))
	return id
}

func containsRequest(reqs []store.IntegrationApprovalRequest, id int64) bool {
	for _, r := range reqs {
		if r.ID == id {
			return true
		}
	}
	return false
}
