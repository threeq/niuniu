// External-tests for ExternalSourceService — exercise project-binding
// CRUD. The legacy browse-cache tests were removed when ExternalSourceService
// dropped BrowseIssues (the AI proxy at /mcp/external-proxy/* now serves
// browse-equivalent calls on demand instead of via a server-side cache).
//
// Uses package service_test (not service) for the same reason as
// external_credential_test.go — niutest imports service.
package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
)

// mockProvider is a hand-rolled integration.Provider for tests. Each
// method delegates to a func field so individual tests can stub only the
// methods they care about. Counters are guarded by a mutex because the
// cache code may eventually fan out concurrent BrowseIssues calls and we
// don't want a flake on -race.
type mockProvider struct {
	mu             sync.Mutex
	listCalls      int
	getCalls       int
	verifyCalls    int
	addCommentN    int
	closeIssueN    int
	listIssuesFn   func(ctx context.Context, c integration.Credential, sk string, opts integration.ListOpts) (*integration.IssuePage, error)
	getIssueFn     func(ctx context.Context, c integration.Credential, sk, eid string) (*integration.ExternalIssue, error)
	verifyFn       func(ctx context.Context, c integration.Credential) error
	addCommentFn   func(ctx context.Context, c integration.Credential, sk, eid, body string) (string, error)
	closeIssueFn   func(ctx context.Context, c integration.Credential, sk, eid, finalBody string) error
	listCommentsFn func(ctx context.Context, c integration.Credential, sk, eid string, opts integration.ListCommentsOpts) ([]integration.ExternalComment, error)
	providerName   integration.ProviderName
}

func (m *mockProvider) Name() integration.ProviderName {
	if m.providerName == "" {
		return integration.ProviderGitHub
	}
	return m.providerName
}

func (m *mockProvider) VerifyCredential(ctx context.Context, c integration.Credential) error {
	m.mu.Lock()
	m.verifyCalls++
	m.mu.Unlock()
	if m.verifyFn != nil {
		return m.verifyFn(ctx, c)
	}
	return nil
}

func (m *mockProvider) ListIssues(ctx context.Context, c integration.Credential, sk string, opts integration.ListOpts) (*integration.IssuePage, error) {
	m.mu.Lock()
	m.listCalls++
	m.mu.Unlock()
	if m.listIssuesFn != nil {
		return m.listIssuesFn(ctx, c, sk, opts)
	}
	return &integration.IssuePage{}, nil
}

func (m *mockProvider) GetIssue(ctx context.Context, c integration.Credential, sk, eid string) (*integration.ExternalIssue, error) {
	m.mu.Lock()
	m.getCalls++
	m.mu.Unlock()
	if m.getIssueFn != nil {
		return m.getIssueFn(ctx, c, sk, eid)
	}
	return &integration.ExternalIssue{ExternalID: eid}, nil
}

func (m *mockProvider) AddComment(ctx context.Context, c integration.Credential, sk, eid, body string) (string, error) {
	m.mu.Lock()
	m.addCommentN++
	m.mu.Unlock()
	if m.addCommentFn != nil {
		return m.addCommentFn(ctx, c, sk, eid, body)
	}
	return "", nil
}

func (m *mockProvider) CloseIssue(ctx context.Context, c integration.Credential, sk, eid, body string) error {
	m.mu.Lock()
	m.closeIssueN++
	m.mu.Unlock()
	if m.closeIssueFn != nil {
		return m.closeIssueFn(ctx, c, sk, eid, body)
	}
	return nil
}

// ListComments stub for the v1.1 external comments snapshot interface.
// Default returns an empty slice; tests that exercise snapshot behavior
// can attach listCommentsFn for custom payloads.
func (m *mockProvider) ListComments(ctx context.Context, c integration.Credential, sk, eid string, opts integration.ListCommentsOpts) ([]integration.ExternalComment, error) {
	if m.listCommentsFn != nil {
		return m.listCommentsFn(ctx, c, sk, eid, opts)
	}
	return []integration.ExternalComment{}, nil
}

// L4 method stubs — Task 3 extended the Provider interface with
// SearchWorkItems / GetWorkItemDetail / UpdateStatus / UpdateAssignees /
// UpdateLabels. mockProvider doesn't exercise those code paths (the L4
// service has its own fakeProvider in external_work_item_test.go), so
// these are no-op stubs that keep the type satisfying the interface.
func (m *mockProvider) SearchWorkItems(_ context.Context, _ integration.Credential, _ string, _ integration.SearchFilters) ([]integration.WorkItemSummary, error) {
	return nil, nil
}
func (m *mockProvider) GetWorkItemDetail(_ context.Context, _ integration.Credential, _, _ string) (*integration.WorkItemDetail, error) {
	return &integration.WorkItemDetail{}, nil
}
func (m *mockProvider) UpdateStatus(_ context.Context, _ integration.Credential, _, _, _ string) error {
	return nil
}
func (m *mockProvider) UpdateAssignees(_ context.Context, _ integration.Credential, _, _ string, _, _ []string) error {
	return nil
}
func (m *mockProvider) UpdateLabels(_ context.Context, _ integration.Credential, _, _ string, _, _ []string) error {
	return nil
}

// callCounts returns a snapshot under the mutex so tests don't read
// torn counters under -race.
func (m *mockProvider) callCounts() (list, get int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listCalls, m.getCalls
}

// buildSourceSvc wires the keyring + a stubbed registry around the
// IsolationEnv DB and returns the source service plus the credential
// service (so tests that need to seed credentials can re-use it).
func buildSourceSvc(t *testing.T, env *niutest.IsolationEnv, prov integration.Provider) (*service.ExternalSourceService, *service.ExternalCredentialService, *service.Authz) {
	t.Helper()
	kr, err := crypto.LoadOrCreate(env.TempPath(t, "integration_secret"))
	if err != nil {
		t.Fatalf("LoadOrCreate keyring: %v", err)
	}
	reg := integration.NewRegistry()
	if prov != nil {
		reg.Register(prov)
	}
	authz := service.NewAuthz(env.Queries(), env.DB)
	creds := service.NewExternalCredentialService(env.Queries(), env.DB, kr, reg)
	svc := service.NewExternalSourceService(env.Queries(), env.DB, creds, reg, authz)
	return svc, creds, authz
}

// TestExternalSource_AddListDelete asserts the CRUD round-trip is sound:
// Add returns a DTO with the right fields, List sees one row, Delete
// removes it. The config map carries through verbatim so the JSON marshal/
// unmarshal preserves keys without coercion.
func TestExternalSource_AddListDelete(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	proj := env.NewProject(t, env.UserA, "P1")

	svc, credSvc, _ := buildSourceSvc(t, env, nil)

	cred, err := credSvc.Create(context.Background(), service.ExternalCredentialUpsertInput{
		OwnerType: "user",
		OwnerID:   env.UserA,
		UserID:    env.UserA,
		Provider:  integration.ProviderGitHub,
		Alias:     "test",
		RawConfig: map[string]any{"token": "ghp_x"},
	})
	if err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	src, err := svc.Add(context.Background(), proj.ID, integration.ProviderGitHub, "acme/foo", cred.ID, map[string]any{"default_state": "open"}, env.UserA)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if src.SourceKey != "acme/foo" {
		t.Fatalf("SourceKey mismatch: got %q", src.SourceKey)
	}
	if src.Provider != integration.ProviderGitHub {
		t.Fatalf("Provider mismatch: got %q", src.Provider)
	}
	if src.Config["default_state"] != "open" {
		t.Fatalf("Config not round-tripped: %+v", src.Config)
	}

	list, err := svc.List(context.Background(), proj.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].SourceKey != "acme/foo" {
		t.Fatalf("List mismatch: %+v", list)
	}

	if err := svc.Delete(context.Background(), src.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	list2, err := svc.List(context.Background(), proj.ID)
	if err != nil {
		t.Fatalf("List after delete: %v", err)
	}
	if len(list2) != 0 {
		t.Fatalf("expected empty after delete, got %d rows", len(list2))
	}
}

// newOrgProject creates an org-owned project (the IsolationEnv NewProject
// helper is user-owned only).
func newOrgProject(t *testing.T, env *niutest.IsolationEnv, orgID int64, name string) store.Project {
	t.Helper()
	p, err := env.Queries().CreateProject(context.Background(), store.CreateProjectParams{
		Name:      name,
		OwnerType: "org",
		OwnerID:   orgID,
	})
	if err != nil {
		t.Fatalf("newOrgProject(%q): %v", name, err)
	}
	return p
}

// TestExternalSource_AddOrgCredToOrgProject: an org-owned credential is bound
// into a project owned by the SAME org by a member (userB) who did not create
// the credential. This is the Wave-2 team-sharing path.
func TestExternalSource_AddOrgCredToOrgProject(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	svc, credSvc, _ := buildSourceSvc(t, env, nil)
	ctx := context.Background()

	// userB is a member of OrgA; the project is owned by OrgA.
	if _, err := env.DB.ExecContext(ctx,
		`INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, 'member')`,
		env.OrgA, env.UserB); err != nil {
		t.Fatalf("add userB to OrgA: %v", err)
	}
	proj := newOrgProject(t, env, env.OrgA, "org-proj")

	// Org-owned credential, created by userA.
	cred, err := credSvc.Create(ctx, service.ExternalCredentialUpsertInput{
		OwnerType: "org", OwnerID: env.OrgA, UserID: env.UserA,
		Provider: integration.ProviderGitHub, Alias: "team",
		RawConfig: map[string]any{"token": "ghp_team"},
	})
	if err != nil {
		t.Fatalf("seed org credential: %v", err)
	}

	// userB (not the creator) binds the org cred into the org project — allowed.
	if _, err := svc.Add(ctx, proj.ID, integration.ProviderGitHub, "acme/foo", cred.ID, nil, env.UserB); err != nil {
		t.Fatalf("Add org cred to org project by member: %v", err)
	}
}

// TestExternalSource_AddOrgCredToForeignProjectRejected: an org cred cannot be
// bound into a project that is NOT owned by that org (here a personal project).
func TestExternalSource_AddOrgCredToForeignProjectRejected(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	svc, credSvc, _ := buildSourceSvc(t, env, nil)
	ctx := context.Background()

	// userA's personal project.
	proj := env.NewProject(t, env.UserA, "personal-proj")

	// Org-owned credential (OrgA).
	cred, err := credSvc.Create(ctx, service.ExternalCredentialUpsertInput{
		OwnerType: "org", OwnerID: env.OrgA, UserID: env.UserA,
		Provider: integration.ProviderGitHub, Alias: "team",
		RawConfig: map[string]any{"token": "ghp_team"},
	})
	if err != nil {
		t.Fatalf("seed org credential: %v", err)
	}

	_, err = svc.Add(ctx, proj.ID, integration.ProviderGitHub, "acme/foo", cred.ID, nil, env.UserA)
	if !errors.Is(err, service.ErrCredentialNotBindable) {
		t.Fatalf("expected ErrCredentialNotBindable binding org cred to personal project, got %v", err)
	}
}

// TestExternalSource_AddPersonalCredOwnership: a personal credential is bindable
// only by its own owner. The owner succeeds; a different caller is rejected.
func TestExternalSource_AddPersonalCredOwnership(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	svc, credSvc, _ := buildSourceSvc(t, env, nil)
	ctx := context.Background()

	// userB joins OrgA and the project is org-owned so userB CAN access the
	// project — proving the rejection is about credential ownership, not project
	// access.
	if _, err := env.DB.ExecContext(ctx,
		`INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, 'member')`,
		env.OrgA, env.UserB); err != nil {
		t.Fatalf("add userB to OrgA: %v", err)
	}
	proj := newOrgProject(t, env, env.OrgA, "org-proj")

	// userA's PERSONAL credential.
	cred, err := credSvc.Create(ctx, service.ExternalCredentialUpsertInput{
		OwnerType: "user", OwnerID: env.UserA, UserID: env.UserA,
		Provider: integration.ProviderGitHub, Alias: "mine",
		RawConfig: map[string]any{"token": "ghp_mine"},
	})
	if err != nil {
		t.Fatalf("seed personal credential: %v", err)
	}

	// userB binding userA's personal cred -> rejected.
	_, err = svc.Add(ctx, proj.ID, integration.ProviderGitHub, "acme/foo", cred.ID, nil, env.UserB)
	if !errors.Is(err, service.ErrCredentialNotBindable) {
		t.Fatalf("expected ErrCredentialNotBindable for non-owner of personal cred, got %v", err)
	}

	// userA (the owner) binding it -> allowed (original behaviour preserved).
	if _, err := svc.Add(ctx, proj.ID, integration.ProviderGitHub, "acme/bar", cred.ID, nil, env.UserA); err != nil {
		t.Fatalf("Add personal cred by owner: %v", err)
	}
}

// TestExternalSource_AddProviderMismatch: provider mismatch still wins over the
// ownership checks (regression for ErrCredentialProviderMismatch).
func TestExternalSource_AddProviderMismatch(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	svc, credSvc, _ := buildSourceSvc(t, env, nil)
	ctx := context.Background()

	proj := env.NewProject(t, env.UserA, "personal-proj")

	cred, err := credSvc.Create(ctx, service.ExternalCredentialUpsertInput{
		OwnerType: "user", OwnerID: env.UserA, UserID: env.UserA,
		Provider: integration.ProviderGitHub, Alias: "gh",
		RawConfig: map[string]any{"token": "ghp_x"},
	})
	if err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	_, err = svc.Add(ctx, proj.ID, integration.ProviderName("tapd"), "acme/foo", cred.ID, nil, env.UserA)
	if !errors.Is(err, service.ErrCredentialProviderMismatch) {
		t.Fatalf("expected ErrCredentialProviderMismatch, got %v", err)
	}
}

