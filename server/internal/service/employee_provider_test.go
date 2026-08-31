package service

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
	_ "modernc.org/sqlite"
)

// newProviderFixture builds a project with a provider bound but NO workspace yet —
// the state a project is in right after someone enables observation on a chat.
func newProviderFixture(t *testing.T) (*store.Queries, int64) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	store.Migrate(db)
	q := store.New(db)
	proj, err := q.CreateProject(context.Background(), store.CreateProjectParams{
		Name: "P", OwnerType: "user", OwnerID: 7,
	})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	return q, proj.ID
}

func seedProvider(t *testing.T, q *store.Queries, name, group string, pos int64, enabled int64, baseURLs string) store.EnvProvider {
	t.Helper()
	p, err := q.CreateEnvProvider(context.Background(), store.CreateEnvProviderParams{
		Name: name, Platform: "custom", BaseUrls: baseURLs,
		ApiKey: "sk-" + name, Model: "m", GroupName: group, GroupPosition: pos,
		Enabled: enabled, OwnerType: "user", OwnerID: 7, Slug: name,
	})
	if err != nil {
		t.Fatalf("provider %s: %v", name, err)
	}
	return p
}

// A project that binds a provider GROUP must yield credentials even with no
// workspace to inherit from. This is the reported bug: the analysis fell back to
// the host's bare credentials and failed with "Not logged in · Please run /login"
// while the project had a provider group configured all along.
func TestEmployeeAnalyzer_ProjectGroupProviderWithoutWorkspace(t *testing.T) {
	q, projectID := newProviderFixture(t)
	ctx := context.Background()
	seedProvider(t, q, "zhipu", "自动切换", 0, 1, `{"anthropic":"https://zhipu.example"}`)
	if err := q.SetProjectEnvProviderGroup(ctx, store.SetProjectEnvProviderGroupParams{
		EnvProviderGroup: "自动切换", ID: projectID,
	}); err != nil {
		t.Fatalf("bind group: %v", err)
	}

	a := &employeeAnalyzer{q: q}
	// WorkspaceID deliberately 0 — nothing to inherit from.
	_, env := a.resolveAgentContext(ctx, EmployeeScope{ProjectID: projectID})
	joined := strings.Join(env, "\n")
	if joined == "" {
		t.Fatal("no provider env resolved from the project's group binding — analysis would run unauthenticated")
	}
	if !strings.Contains(joined, "https://zhipu.example") {
		t.Errorf("bound provider's base_url missing: %q", joined)
	}
	if !strings.Contains(joined, "sk-zhipu") {
		t.Errorf("bound provider's api key missing: %q", joined)
	}
	// niuniu's own control knobs are meaningless to a generation subprocess.
	if strings.Contains(joined, "NIUNIU_") {
		t.Errorf("leaked a NIUNIU_ control var: %q", joined)
	}
}

// A specifically-bound provider (not a group) must work the same way.
func TestEmployeeAnalyzer_ProjectSingleProviderWithoutWorkspace(t *testing.T) {
	q, projectID := newProviderFixture(t)
	ctx := context.Background()
	p := seedProvider(t, q, "deepseek", "", 0, 1, `{"anthropic":"https://ds.example"}`)
	if err := q.SetProjectEnvProvider(ctx, store.SetProjectEnvProviderParams{
		EnvProviderID: sql.NullInt64{Int64: p.ID, Valid: true}, ID: projectID,
	}); err != nil {
		t.Fatalf("bind provider: %v", err)
	}

	a := &employeeAnalyzer{q: q}
	_, env := a.resolveAgentContext(ctx, EmployeeScope{ProjectID: projectID})
	if !strings.Contains(strings.Join(env, "\n"), "https://ds.example") {
		t.Errorf("single provider binding not resolved: %v", env)
	}
}

// A project binding NO provider must resolve to nothing, preserving the previous
// behavior (host / global one-shot preset credentials) for installs that never
// configured providers.
func TestEmployeeAnalyzer_NoProjectBindingStaysEmpty(t *testing.T) {
	q, projectID := newProviderFixture(t)
	seedProvider(t, q, "unbound", "", 0, 1, `{"anthropic":"https://x.example"}`)
	a := &employeeAnalyzer{q: q}
	if _, env := a.resolveAgentContext(context.Background(), EmployeeScope{ProjectID: projectID}); len(env) != 0 {
		t.Errorf("unbound project resolved provider env: %v", env)
	}
}

// Group member selection must mirror sceneenv: lowest group_position wins, and a
// disabled member is skipped rather than chosen.
func TestPickProjectProvider_GroupOrderAndUsability(t *testing.T) {
	url := `{"anthropic":"https://a.example"}`
	q, _ := newProviderFixture(t)
	disabled := seedProvider(t, q, "first-but-disabled", "g", 0, 0, url)
	second := seedProvider(t, q, "second", "g", 1, 1, url)
	third := seedProvider(t, q, "third", "g", 2, 1, url)
	all := []store.EnvProvider{disabled, third, second}

	got, ok := pickProjectProvider(all, "g", 0, "anthropic", time.Unix(1750000000, 0))
	if !ok {
		t.Fatal("no provider picked from a group with usable members")
	}
	if got.Name != "second" {
		t.Errorf("picked %q, want the lowest-position ENABLED member (second)", got.Name)
	}

	// A provider with no base_url for the protocol cannot serve this CLI.
	noURL := seedProvider(t, q, "wrong-protocol", "h", 0, 1, `{"openai":"https://o.example"}`)
	if _, ok := pickProjectProvider([]store.EnvProvider{noURL}, "h", 0, "anthropic", time.Unix(1750000000, 0)); ok {
		t.Error("picked a provider that cannot serve the anthropic protocol")
	}

	// Group bound but wholly unusable, with an explicit provider also bound: the
	// explicit binding is the documented fallback.
	if got, ok := pickProjectProvider([]store.EnvProvider{noURL, second}, "h", second.ID, "anthropic", time.Unix(1750000000, 0)); !ok || got.Name != "second" {
		t.Errorf("explicit binding not used when the group is unusable: got %+v ok=%v", got.Name, ok)
	}
}
