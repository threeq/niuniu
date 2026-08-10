// Tests for ExternalProxyService.resolveCredential — the credential-resolution
// fork that fixes the cross-member 401. In-workspace MCP agents (WorkspaceID>0)
// resolve the credential via the PROJECT BINDING (project_external_sources)
// using GetBoundByID (no per-caller ownership check), so any project member's
// agent can use the one credential the project bound — even though that
// credential belongs to a different member. SPA direct calls (WorkspaceID==0)
// keep the legacy "caller's own credential" behaviour.
//
// External (service_test) package so the shared niutest IsolationEnv fixtures
// are usable (internal/testing imports service, which would be an import cycle
// from package service). resolveCredential is reached via the
// ResolveCredentialForTest shim in export_test.go.
package service_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
)

// proxyTestEnv bundles the proxy service under test with the ids the
// resolveCredential assertions need.
type proxyTestEnv struct {
	env    *niutest.IsolationEnv
	svc    *service.ExternalProxyService
	userA  int64
	userB  int64
	userC  int64 // userC: NOT a member of the owning org (authz-reject case)
	wsID   int64
	projID int64
	credA  int64 // credA: source_key "123"
	credA2 int64 // credA2: source_key "456"
}

// newProxyTestEnv builds the cross-member reuse scenario. The project is owned
// by an ORG (OrgA from the isolation fixture); userA and userB are both members
// of that org, so either's agent may reuse the project-bound credential. userC
// is a fresh user that is NOT a member of the org — used to prove the IDOR
// hardening rejects callers who can't access the project. The provider name
// (tapd) is only a string key in project_external_sources; both credentials are
// owned by userA, with one tapd binding (project_external_sources -> credA).
// Extra bindings are added per-test.
func newProxyTestEnv(t *testing.T) *proxyTestEnv {
	t.Helper()
	env := niutest.NewIsolationEnv(t)
	kr, err := crypto.LoadOrCreate(env.TempPath(t, "integration_secret"))
	if err != nil {
		t.Fatalf("LoadOrCreate keyring: %v", err)
	}
	reg := integration.NewRegistry()
	credSvc := service.NewExternalCredentialService(env.Queries(), env.DB, kr, reg)
	provSvc := service.NewExternalProviderService(env.Queries(), env.DB)
	authz := service.NewAuthz(env.Queries(), env.DB)
	svc := service.NewExternalProxyService(env.Queries(), env.DB, provSvc, credSvc, authz)

	ctx := context.Background()
	userA := env.UserA
	userB := env.UserB

	// userB joins OrgA as a member so the project-bound credential is reusable
	// across members (the cross-member 401 fix); userA is already OrgA's owner.
	if _, err := env.DB.ExecContext(ctx,
		`INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, 'member')`,
		env.OrgA, userB); err != nil {
		t.Fatalf("add userB to OrgA: %v", err)
	}
	// userC is a brand-new user with NO org membership — must be rejected.
	res, err := env.DB.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, role) VALUES ('user-c', 'x', 'member')`)
	if err != nil {
		t.Fatalf("create userC: %v", err)
	}
	userC, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("userC last insert id: %v", err)
	}

	// userA owns both tapd credentials.
	cA, err := credSvc.Create(ctx, service.ExternalCredentialUpsertInput{
		OwnerType: "user", OwnerID: userA, UserID: userA,
		Provider:  integration.ProviderName("tapd"),
		Alias:     "tapd-A",
		RawConfig: map[string]any{"token": "tok-A"},
	})
	if err != nil {
		t.Fatalf("Create credA: %v", err)
	}
	cA2, err := credSvc.Create(ctx, service.ExternalCredentialUpsertInput{
		OwnerType: "user", OwnerID: userA, UserID: userA,
		Provider:  integration.ProviderName("tapd"),
		Alias:     "tapd-A2",
		RawConfig: map[string]any{"token": "tok-A2"},
	})
	if err != nil {
		t.Fatalf("Create credA2: %v", err)
	}

	// project -> column -> issue -> workspace chain so
	// GetProjectIDForWorkspace(wsID) resolves to projID. The project is owned by
	// OrgA (owner_type='org', owner_id=OrgA) so org members (userA, userB) can
	// access it while non-members (userC) cannot.
	proj, err := env.Queries().CreateProject(ctx, store.CreateProjectParams{
		Name:      "binding",
		OwnerType: "org",
		OwnerID:   env.OrgA,
	})
	if err != nil {
		t.Fatalf("CreateProject (org-owned): %v", err)
	}
	col := env.NewColumn(t, proj.ID, "todo", "todo")
	issue := env.SeedExternalIssue(t, col.ID, "tapd", "ws-1")
	ws, err := env.Queries().CreateWorkspace(ctx, store.CreateWorkspaceParams{
		IssueID:   sql.NullInt64{Int64: issue.ID, Valid: true},
		Name:      "ws-1",
		Path:      "/tmp/ws-1",
		Status:    "created",
		OwnerType: "user",
		OwnerID:   userA,
		CreatedBy: sql.NullInt64{Int64: userA, Valid: true},
		CliType:   "claude",
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	// One tapd binding -> credA, source_key "123".
	if _, err := env.DB.ExecContext(ctx,
		`INSERT INTO project_external_sources (project_id, provider, source_key, credential_id)
		 VALUES (?, ?, ?, ?)`, proj.ID, "tapd", "123", cA.ID); err != nil {
		t.Fatalf("insert binding credA: %v", err)
	}

	return &proxyTestEnv{
		env:    env,
		svc:    svc,
		userA:  userA,
		userB:  userB,
		userC:  userC,
		wsID:   ws.ID,
		projID: proj.ID,
		credA:  cA.ID,
		credA2: cA2.ID,
	}
}

// addSecondBinding inserts a second tapd binding (source_key "456" -> credA2)
// so the multi-binding disambiguation path can be exercised.
func (p *proxyTestEnv) addSecondBinding(t *testing.T) {
	t.Helper()
	if _, err := p.env.DB.ExecContext(context.Background(),
		`INSERT INTO project_external_sources (project_id, provider, source_key, credential_id)
		 VALUES (?, ?, ?, ?)`, p.projID, "tapd", "456", p.credA2); err != nil {
		t.Fatalf("insert binding credA2: %v", err)
	}
}

// TestResolveCredential_ByBinding: userB (no own tapd credential) calls with
// WorkspaceID>0 and resolves the project-bound credA (owned by userA). This is
// the cross-member 401 fix.
func TestResolveCredential_ByBinding(t *testing.T) {
	p := newProxyTestEnv(t)
	cred, err := p.svc.ResolveCredentialForTest(context.Background(), service.ProxyInput{
		UserID:      p.userB,
		Provider:    "tapd",
		WorkspaceID: p.wsID,
	})
	if err != nil {
		t.Fatalf("resolveCredential by binding: %v", err)
	}
	if cred.ID != p.credA {
		t.Fatalf("expected bound credA id=%d, got id=%d", p.credA, cred.ID)
	}
	if tok, _ := cred.Config["token"].(string); tok != "tok-A" {
		t.Fatalf("expected decrypted token tok-A, got %q", tok)
	}
}

// TestResolveCredential_NonMemberRejected: userC is NOT a member of the org
// that owns the project, so even with a valid WorkspaceID the IDOR hardening
// must refuse to resolve (and thus use) the project-bound credential. Without
// the CanAccessProject gate, a revoked/cross-project identity could drive
// another project's credential server-side.
func TestResolveCredential_NonMemberRejected(t *testing.T) {
	p := newProxyTestEnv(t)
	_, err := p.svc.ResolveCredentialForTest(context.Background(), service.ProxyInput{
		UserID:      p.userC,
		Provider:    "tapd",
		WorkspaceID: p.wsID,
	})
	if err == nil {
		t.Fatal("expected non-member to be rejected, got nil error")
	}
	if !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for non-member, got %v", err)
	}
}

// TestResolveCredential_MultiBindingNeedsSourceKey: two tapd bindings in the
// project. Without SourceKey resolution is ambiguous (error mentions
// source_key); SourceKey="456" selects credA2.
func TestResolveCredential_MultiBindingNeedsSourceKey(t *testing.T) {
	p := newProxyTestEnv(t)
	p.addSecondBinding(t)

	_, err := p.svc.ResolveCredentialForTest(context.Background(), service.ProxyInput{
		UserID:      p.userB,
		Provider:    "tapd",
		WorkspaceID: p.wsID,
	})
	if err == nil {
		t.Fatal("expected ambiguity error with two bindings and no source_key")
	}
	if !strings.Contains(err.Error(), "source_key") {
		t.Fatalf("expected error to mention source_key, got %v", err)
	}
	// The ambiguity error must enumerate the available source_keys so the agent
	// can retry with a valid one instead of dead-ending on "specify source_key".
	if !errors.Is(err, service.ErrAmbiguousSource) {
		t.Fatalf("expected ErrAmbiguousSource sentinel, got %v", err)
	}
	var ambErr *service.AmbiguousSourceError
	if !errors.As(err, &ambErr) {
		t.Fatalf("expected *AmbiguousSourceError, got %T (%v)", err, err)
	}
	if got := strings.Join(ambErr.AvailableKeys, ","); !strings.Contains(got, "123") || !strings.Contains(got, "456") {
		t.Fatalf("expected available keys to include 123 and 456, got %q", got)
	}
	if !strings.Contains(err.Error(), "123") || !strings.Contains(err.Error(), "456") {
		t.Fatalf("expected error text to list available keys 123 and 456, got %v", err)
	}

	cred, err := p.svc.ResolveCredentialForTest(context.Background(), service.ProxyInput{
		UserID:      p.userB,
		Provider:    "tapd",
		WorkspaceID: p.wsID,
		SourceKey:   "456",
	})
	if err != nil {
		t.Fatalf("resolveCredential with source_key=456: %v", err)
	}
	if cred.ID != p.credA2 {
		t.Fatalf("expected credA2 id=%d, got id=%d", p.credA2, cred.ID)
	}
	if tok, _ := cred.Config["token"].(string); tok != "tok-A2" {
		t.Fatalf("expected decrypted token tok-A2, got %q", tok)
	}
}

// TestResolveCredential_WrongSourceKeyListsAvailable: a source_key that doesn't
// match any binding (while the provider DOES have sources bound) is a fixable
// client mistake, not a missing-source condition. The error must be an
// AmbiguousSourceError that enumerates the valid keys so the agent can correct
// itself, rather than the dead-end "no source configured" (NO_SOURCE/404).
func TestResolveCredential_WrongSourceKeyListsAvailable(t *testing.T) {
	p := newProxyTestEnv(t)
	p.addSecondBinding(t)

	_, err := p.svc.ResolveCredentialForTest(context.Background(), service.ProxyInput{
		UserID:      p.userB,
		Provider:    "tapd",
		WorkspaceID: p.wsID,
		SourceKey:   "999", // not one of {123, 456}
	})
	if err == nil {
		t.Fatal("expected error for unknown source_key")
	}
	if errors.Is(err, service.ErrNoProjectSource) {
		t.Fatalf("a bad source_key must not be reported as a missing source, got %v", err)
	}
	var ambErr *service.AmbiguousSourceError
	if !errors.As(err, &ambErr) {
		t.Fatalf("expected *AmbiguousSourceError for a bad source_key, got %T (%v)", err, err)
	}
	if !strings.Contains(err.Error(), "123") || !strings.Contains(err.Error(), "456") {
		t.Fatalf("expected error to list available keys 123 and 456, got %v", err)
	}
}

// TestResolveCredential_SPAFallbackToCaller: WorkspaceID==0 (SPA direct /
// settings self-test) falls back to the caller's own credential for the
// provider. userA owns credA (and credA2); the fallback picks userA's first.
func TestResolveCredential_SPAFallbackToCaller(t *testing.T) {
	p := newProxyTestEnv(t)
	cred, err := p.svc.ResolveCredentialForTest(context.Background(), service.ProxyInput{
		UserID:   p.userA,
		Provider: "tapd",
		// WorkspaceID omitted (==0): legacy caller-credential path.
	})
	if err != nil {
		t.Fatalf("resolveCredential SPA fallback: %v", err)
	}
	// The fallback loads the caller's own credential; with userA owning credA
	// and credA2, ListByProvider returns them and the first is decrypted.
	if cred.ID != p.credA && cred.ID != p.credA2 {
		t.Fatalf("expected one of userA's creds (%d/%d), got id=%d", p.credA, p.credA2, cred.ID)
	}
}
