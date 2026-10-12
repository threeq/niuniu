// Tests for the team-edition org-credential usage policy + credential-link
// owner binding (issue #742). External (service_test) package so the shared
// niutest IsolationEnv fixtures are usable.
package service_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
)

// policyEnv bundles the policy service with the fixture ids the tests need.
type policyEnv struct {
	env      *niutest.IsolationEnv
	creds    *service.ExternalCredentialService
	policy   *service.CredentialUsagePolicyService
	authz    *service.Authz
	projOrgA int64
	wsOrgA   int64
	credOrgA int64
}

func newPolicyEnv(t *testing.T) *policyEnv {
	t.Helper()
	env := niutest.NewIsolationEnv(t)
	kr, err := crypto.LoadOrCreate(env.TempPath(t, "integration_secret"))
	require.NoError(t, err)
	reg := integration.NewRegistry()
	authz := service.NewAuthz(env.Queries(), env.DB)
	creds := service.NewExternalCredentialService(env.Queries(), env.DB, kr, reg)
	policy := service.NewCredentialUsagePolicyService(env.Queries(), env.DB, creds, authz)

	ctx := context.Background()
	proj := insertOrgProject(t, env, env.OrgA, "policy-proj")
	ws := insertOrgWorkspace(t, env, env.OrgA, proj)

	cred, err := creds.Create(ctx, service.ExternalCredentialUpsertInput{
		OwnerType: "org", OwnerID: env.OrgA, UserID: env.UserA,
		Provider:  integration.ProviderName("tapd"),
		Alias:     "org-a-cred",
		RawConfig: map[string]any{"token": "tok"},
	})
	require.NoError(t, err)

	return &policyEnv{env: env, creds: creds, policy: policy, authz: authz,
		projOrgA: proj, wsOrgA: ws, credOrgA: cred.ID}
}

// insertOrgProject inserts an org-owned project and returns its id.
func insertOrgProject(t *testing.T, env *niutest.IsolationEnv, orgID int64, name string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, store.Wrap(env.DB).QueryRowContext(context.Background(),
		`INSERT INTO projects (owner_type, owner_id, name) VALUES ('org', ?, ?) RETURNING id`,
		orgID, name).Scan(&id))
	return id
}

// insertOrgWorkspace builds project -> column -> issue -> workspace so
// GetProjectIDForWorkspace (used by the grant-scope join) resolves back to the
// project. Returns the workspace id.
func insertOrgWorkspace(t *testing.T, env *niutest.IsolationEnv, orgID, projectID int64) int64 {
	t.Helper()
	col := env.NewColumn(t, projectID, "todo", "created")
	issue := env.SeedExternalIssue(t, col.ID, "tapd", "ws-1")
	ws, err := env.Queries().CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		IssueID:   sql.NullInt64{Int64: issue.ID, Valid: true},
		Name:      "ws-1",
		Path:      "/tmp/ws-1",
		Status:    "created",
		OwnerType: "org",
		OwnerID:   orgID,
		CreatedBy: sql.NullInt64{Int64: env.UserA, Valid: true},
		CliType:   "claude",
	})
	require.NoError(t, err)
	return ws.ID
}

// An org credential is unusable everywhere until an admin grants a scope
// (default-minimal). Personal credentials are always allowed.
func TestCredentialUsagePolicy_DefaultMinimal(t *testing.T) {
	p := newPolicyEnv(t)
	ctx := context.Background()

	allowed, err := p.policy.IsAllowed(ctx, p.credOrgA, p.projOrgA, 0)
	require.NoError(t, err)
	assert.False(t, allowed, "org credential must be unusable without a grant")

	// Personal credential is exempt.
	personal, err := p.creds.Create(ctx, service.ExternalCredentialUpsertInput{
		OwnerType: "user", OwnerID: p.env.UserA, UserID: p.env.UserA,
		Provider: integration.ProviderName("tapd"), Alias: "mine",
		RawConfig: map[string]any{"token": "tok"},
	})
	require.NoError(t, err)
	allowed, err = p.policy.IsAllowed(ctx, personal.ID, p.projOrgA, 0)
	require.NoError(t, err)
	assert.True(t, allowed, "personal credential must not be policy-gated")
}

// Granting a project authorizes the whole project; revoking takes it back.
func TestCredentialUsagePolicy_GrantProjectScope(t *testing.T) {
	p := newPolicyEnv(t)
	ctx := context.Background()

	require.NoError(t, p.policy.Grant(ctx, p.env.UserA, p.credOrgA, service.GrantScopeProject, p.projOrgA))
	allowed, err := p.policy.IsAllowed(ctx, p.credOrgA, p.projOrgA, 0)
	require.NoError(t, err)
	assert.True(t, allowed)

	require.NoError(t, p.policy.Revoke(ctx, p.env.UserA, p.credOrgA, service.GrantScopeProject, p.projOrgA))
	allowed, err = p.policy.IsAllowed(ctx, p.credOrgA, p.projOrgA, 0)
	require.NoError(t, err)
	assert.False(t, allowed, "revoking the grant must immediately de-authorize")
}

// A workspace grant authorizes binding into the project and narrows USE to
// that workspace only.
func TestCredentialUsagePolicy_WorkspaceScope(t *testing.T) {
	p := newPolicyEnv(t)
	ctx := context.Background()
	require.NoError(t, p.policy.Grant(ctx, p.env.UserA, p.credOrgA, service.GrantScopeWorkspace, p.wsOrgA))

	// Binding into the project is authorized because a workspace in it is granted.
	ok, err := p.policy.IsProjectAuthorized(ctx, p.credOrgA, p.projOrgA)
	require.NoError(t, err)
	assert.True(t, ok)

	// The granted workspace may use it; another workspace (id 0 stands for "none")
	// with no project grant may not.
	allowed, err := p.policy.IsAllowed(ctx, p.credOrgA, p.projOrgA, p.wsOrgA)
	require.NoError(t, err)
	assert.True(t, allowed)
	allowed, err = p.policy.IsAllowed(ctx, p.credOrgA, p.projOrgA, 999999)
	require.NoError(t, err)
	assert.False(t, allowed)
}

// Non-admins cannot grant; and a grant cannot target another org's scope.
func TestCredentialUsagePolicy_GrantAuthorization(t *testing.T) {
	p := newPolicyEnv(t)
	ctx := context.Background()

	// userB is OrgB's owner, not an admin of OrgA -> forbidden on A's credential.
	err := p.policy.Grant(ctx, p.env.UserB, p.credOrgA, service.GrantScopeProject, p.projOrgA)
	assert.ErrorIs(t, err, service.ErrForbidden)

	// A grant on a personal credential is rejected outright.
	personal, err := p.creds.Create(ctx, service.ExternalCredentialUpsertInput{
		OwnerType: "user", OwnerID: p.env.UserA, UserID: p.env.UserA,
		Provider: integration.ProviderName("tapd"), Alias: "mine2",
		RawConfig: map[string]any{"token": "tok"},
	})
	require.NoError(t, err)
	err = p.policy.Grant(ctx, p.env.UserA, personal.ID, service.GrantScopeProject, p.projOrgA)
	assert.ErrorIs(t, err, service.ErrGrantPersonalCredential)

	// Cross-owner scope: userA (OrgA owner) owns the credential, but targeting an
	// OrgB project it cannot even see is refused (invisible -> 404, no leak).
	projOrgB := insertOrgProject(t, p.env, p.env.OrgB, "orgb-proj")
	err = p.policy.Grant(ctx, p.env.UserA, p.credOrgA, service.GrantScopeProject, projOrgB)
	assert.ErrorIs(t, err, service.ErrForbidden)

	// If the caller can access the scope but it belongs to a different owner, the
	// grant is refused as cross-owner.
	_, err = p.env.DB.ExecContext(ctx,
		`INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, 'member')`, p.env.OrgB, p.env.UserA)
	require.NoError(t, err)
	p.authz.InvalidateUser(p.env.UserA) // raw insert bypassed the membership cache
	err = p.policy.Grant(ctx, p.env.UserA, p.credOrgA, service.GrantScopeProject, projOrgB)
	assert.ErrorIs(t, err, service.ErrGrantScopeNotOwned)
}

// The binding path is API-enforced: an org credential cannot be attached to a
// project without a grant; after the grant the binding succeeds.
func TestExternalSource_OrgCredentialBindingEnforced(t *testing.T) {
	p := newPolicyEnv(t)
	ctx := context.Background()
	reg := integration.NewRegistry()
	svc := service.NewExternalSourceService(p.env.Queries(), p.env.DB, p.creds, reg, p.authz, p.policy)

	_, err := svc.Add(ctx, p.projOrgA, integration.ProviderName("tapd"), "k1", p.credOrgA, nil, p.env.UserA)
	assert.ErrorIs(t, err, service.ErrCredentialNotGranted, "ungranted org credential must not bind")

	require.NoError(t, p.policy.Grant(ctx, p.env.UserA, p.credOrgA, service.GrantScopeProject, p.projOrgA))
	_, err = svc.Add(ctx, p.projOrgA, integration.ProviderName("tapd"), "k1", p.credOrgA, nil, p.env.UserA)
	require.NoError(t, err, "granted org credential must bind")

	// A PERSONAL credential still binds with no grant in sight (regression guard).
	personal, err := p.creds.Create(ctx, service.ExternalCredentialUpsertInput{
		OwnerType: "user", OwnerID: p.env.UserA, UserID: p.env.UserA,
		Provider: integration.ProviderName("tapd"), Alias: "mine3",
		RawConfig: map[string]any{"token": "tok"},
	})
	require.NoError(t, err)
	_, err = svc.Add(ctx, p.projOrgA, integration.ProviderName("tapd"), "k2", personal.ID, nil, p.env.UserA)
	require.NoError(t, err)
}

// The proxy USE path is API-enforced: an org credential bound to a project is
// refused at call time until its scope is granted.
func TestProxy_OrgCredentialUseEnforced(t *testing.T) {
	p := newPolicyEnv(t)
	ctx := context.Background()
	provSvc := service.NewExternalProviderService(p.env.Queries(), p.env.DB)
	proxy := service.NewExternalProxyService(p.env.Queries(), p.env.DB, provSvc, p.creds, p.authz, p.policy)

	// Bind directly (bypassing Add) to prove the CALL path enforces too.
	_, err := p.env.DB.ExecContext(ctx,
		`INSERT INTO project_external_sources (project_id, provider, source_key, credential_id)
		 VALUES (?, 'tapd', 'k1', ?)`, p.projOrgA, p.credOrgA)
	require.NoError(t, err)

	in := service.ProxyInput{UserID: p.env.UserA, Provider: "tapd", Method: "GET", Path: "/x", WorkspaceID: p.wsOrgA}
	_, err = proxy.ResolveCredentialForTest(ctx, in)
	var proxyErr *service.ProxyError
	require.ErrorAs(t, err, &proxyErr)
	assert.Equal(t, "credential_not_authorized", proxyErr.Code)

	// After an admin grants the project, resolution succeeds.
	require.NoError(t, p.policy.Grant(ctx, p.env.UserA, p.credOrgA, service.GrantScopeProject, p.projOrgA))
	cred, err := proxy.ResolveCredentialForTest(ctx, in)
	require.NoError(t, err)
	assert.Equal(t, p.credOrgA, cred.ID)

	// Using the org credential appended an org-scoped usage audit row.
	var used int
	require.NoError(t, p.env.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM org_audit_log WHERE org_id = ? AND action = 'integration.credential.used'`,
		p.env.OrgA).Scan(&used))
	assert.GreaterOrEqual(t, used, 1)
}

// newOnboardingSvc wires the credential-onboarding service (provider-credential
// kind only; the data-source collaborator is unused on that path).
func newOnboardingSvc(t *testing.T, env *niutest.IsolationEnv, creds *service.ExternalCredentialService) *service.CredentialOnboardingService {
	t.Helper()
	return service.NewCredentialOnboardingService(env.Queries(), creds, nil)
}

// A link bound to one owner cannot be redeemed after the project moved to a
// different owner (cross-owner submit is refused).
func TestCredentialOnboarding_CrossOwnerSubmitRejected(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	ctx := context.Background()
	kr, err := crypto.LoadOrCreate(env.TempPath(t, "integration_secret"))
	require.NoError(t, err)
	creds := service.NewExternalCredentialService(env.Queries(), env.DB, kr, integration.NewRegistry())
	onb := newOnboardingSvc(t, env, creds)

	proj := insertOrgProject(t, env, env.OrgA, "link-proj")
	token, err := onb.Issue(ctx, proj, env.UserA, service.CredentialKindProviderCredential, "t",
		[]string{"token"}, map[string]any{"provider": "github", "alias": "a"})
	require.NoError(t, err)

	// Drift the project to another owner.
	_, err = env.DB.ExecContext(ctx, `UPDATE projects SET owner_id = ? WHERE id = ?`, env.OrgB, proj)
	require.NoError(t, err)

	_, err = onb.Submit(ctx, token, map[string]any{"token": "ghp_abc"})
	assert.ErrorIs(t, err, service.ErrCredentialOwnerMismatch)
}

// The link lifecycle is auditable: issuing for an org target writes an
// org-scoped audit row, and a member removal drops the member's pending links.
func TestCredentialOnboarding_OwnerAuditAndReclaim(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	ctx := context.Background()
	kr, err := crypto.LoadOrCreate(env.TempPath(t, "integration_secret"))
	require.NoError(t, err)
	creds := service.NewExternalCredentialService(env.Queries(), env.DB, kr, integration.NewRegistry())
	onb := newOnboardingSvc(t, env, creds)

	proj := insertOrgProject(t, env, env.OrgA, "link-proj-2")
	token, err := onb.Issue(ctx, proj, env.UserA, service.CredentialKindProviderCredential, "t",
		[]string{"token"}, map[string]any{"provider": "github", "alias": "a"})
	require.NoError(t, err)

	var issued int
	require.NoError(t, env.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM org_audit_log WHERE org_id = ? AND action = 'credential_link.issued'`,
		env.OrgA).Scan(&issued))
	assert.Equal(t, 1, issued)

	// Member removal reclaims the pending link.
	n, err := onb.RevokeUnusedOnboardingTokensByIssuerInOrg(ctx, env.OrgA, env.UserA)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	_, err = onb.Info(ctx, token)
	assert.ErrorIs(t, err, service.ErrCredentialTokenInvalid)

	// A link the member issued in ANOTHER org survives the OrgA removal.
	projB := insertOrgProject(t, env, env.OrgB, "link-proj-b")
	tokenB, err := onb.Issue(ctx, projB, env.UserA, service.CredentialKindProviderCredential, "t",
		[]string{"token"}, map[string]any{"provider": "github", "alias": "a"})
	require.NoError(t, err)
	_, err = onb.RevokeUnusedOnboardingTokensByIssuerInOrg(ctx, env.OrgA, env.UserA)
	require.NoError(t, err)
	_, err = onb.Info(ctx, tokenB)
	require.NoError(t, err, "a link bound to another org must not be reclaimed")
}
