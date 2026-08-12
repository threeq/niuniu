package sceneenv

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// fakeQuerier is a minimal in-memory stand-in for *store.Queries, exercising
// the merge logic without a database.
type fakeQuerier struct {
	env           []store.WorkspaceEnv
	envErr        error
	projection    store.WorkspaceSceneProjection
	projErr       error
	accounts      []store.EnvAccount
	providers     []store.EnvProvider
	boundProvider *store.EnvProvider
	cliType       string
}

func (f fakeQuerier) ListWorkspaceEnv(_ context.Context, _ int64) ([]store.WorkspaceEnv, error) {
	return f.env, f.envErr
}

func (f fakeQuerier) GetProjection(_ context.Context, _ int64) (store.WorkspaceSceneProjection, error) {
	return f.projection, f.projErr
}

func (f fakeQuerier) GetWorkspace(_ context.Context, _ int64) (store.Workspace, error) {
	return store.Workspace{}, nil // personal owner (OwnerID=0) — tests inject accounts directly
}

func (f fakeQuerier) ListEnvAccountsForOwners(_ context.Context, _ store.ListEnvAccountsForOwnersParams) ([]store.EnvAccount, error) {
	return f.accounts, nil
}

func (f fakeQuerier) ListEnvProvidersForOwners(_ context.Context, _ store.ListEnvProvidersForOwnersParams) ([]store.EnvProvider, error) {
	return f.providers, nil
}

func (f fakeQuerier) GetEnvProvider(_ context.Context, id int64) (store.EnvProvider, error) {
	if f.boundProvider != nil && f.boundProvider.ID == id {
		return *f.boundProvider, nil
	}
	for _, p := range f.providers {
		if p.ID == id {
			return p, nil
		}
	}
	return store.EnvProvider{}, sql.ErrNoRows
}

func (f fakeQuerier) GetWorkspaceCliType(_ context.Context, _ int64) (string, error) {
	if f.cliType == "" {
		return "claude", nil
	}
	return f.cliType, nil
}

func (f fakeQuerier) GetWorkspaceEnvProviderID(_ context.Context, _ int64) (int64, error) {
	if f.boundProvider != nil {
		return f.boundProvider.ID, nil
	}
	return 0, nil
}

func envMap(rows []store.WorkspaceEnv) map[string]string {
	m := map[string]string{}
	for _, r := range rows {
		m[r.Key] = r.Value
	}
	return m
}

func TestResolve_SceneEnvAppliedWhenWorkspaceHasNone(t *testing.T) {
	q := fakeQuerier{
		env: []store.WorkspaceEnv{{WorkspaceID: 7, Key: "NIUNIU_PERMISSION_MODE", Value: "autohost"}},
		projection: store.WorkspaceSceneProjection{ProjectedDefinition: `{
			"assets": {"env_presets": [
				{"slug": "provider", "env": {"ANTHROPIC_BASE_URL": "https://scene.example", "ANTHROPIC_AUTH_TOKEN": "scene-token"}}
			]}
		}`},
	}

	rows, err := Resolve(context.Background(), q, 7)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	got := envMap(rows)
	if got["ANTHROPIC_BASE_URL"] != "https://scene.example" {
		t.Errorf("scene ANTHROPIC_BASE_URL not applied: got %q", got["ANTHROPIC_BASE_URL"])
	}
	if got["ANTHROPIC_AUTH_TOKEN"] != "scene-token" {
		t.Errorf("scene ANTHROPIC_AUTH_TOKEN not applied: got %q", got["ANTHROPIC_AUTH_TOKEN"])
	}
	if got["NIUNIU_PERMISSION_MODE"] != "autohost" {
		t.Errorf("explicit workspace_env dropped: got %q", got["NIUNIU_PERMISSION_MODE"])
	}
}

func TestResolve_ExplicitWorkspaceEnvWinsOverScene(t *testing.T) {
	q := fakeQuerier{
		env: []store.WorkspaceEnv{{WorkspaceID: 7, Key: "ANTHROPIC_BASE_URL", Value: "https://user.override"}},
		projection: store.WorkspaceSceneProjection{ProjectedDefinition: `{
			"assets": {"env_presets": [
				{"slug": "provider", "env": {"ANTHROPIC_BASE_URL": "https://scene.example"}}
			]}
		}`},
	}

	rows, err := Resolve(context.Background(), q, 7)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	got := envMap(rows)
	if got["ANTHROPIC_BASE_URL"] != "https://user.override" {
		t.Errorf("explicit workspace_env should win: got %q", got["ANTHROPIC_BASE_URL"])
	}
	// Exactly one row for the key — no duplicate KEY=VALUE entries.
	count := 0
	for _, r := range rows {
		if r.Key == "ANTHROPIC_BASE_URL" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly one ANTHROPIC_BASE_URL row, got %d", count)
	}
}

func TestResolve_LaterEnvPresetWins(t *testing.T) {
	q := fakeQuerier{
		projection: store.WorkspaceSceneProjection{ProjectedDefinition: `{
			"assets": {"env_presets": [
				{"slug": "a", "env": {"K": "first"}},
				{"slug": "b", "env": {"K": "second"}}
			]}
		}`},
	}

	rows, err := Resolve(context.Background(), q, 7)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if got := envMap(rows)["K"]; got != "second" {
		t.Errorf("later env_preset should win: got %q", got)
	}
}

func TestResolve_NoProjectionReturnsBaseUnchanged(t *testing.T) {
	q := fakeQuerier{
		env:     []store.WorkspaceEnv{{WorkspaceID: 7, Key: "FOO", Value: "bar"}},
		projErr: sql.ErrNoRows,
	}

	rows, err := Resolve(context.Background(), q, 7)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if len(rows) != 1 || rows[0].Key != "FOO" || rows[0].Value != "bar" {
		t.Errorf("expected base env unchanged, got %+v", rows)
	}
}

func TestResolve_ListEnvErrorPropagates(t *testing.T) {
	q := fakeQuerier{envErr: sql.ErrConnDone}
	if _, err := Resolve(context.Background(), q, 7); err == nil {
		t.Fatal("expected error to propagate when ListWorkspaceEnv fails")
	}
}

func TestResolve_AccountRefSubstituted(t *testing.T) {
	q := fakeQuerier{
		env: []store.WorkspaceEnv{
			{WorkspaceID: 7, Key: "ANTHROPIC_AUTH_TOKEN", Value: "${ACCOUNT:DeepSeek}"},
			{WorkspaceID: 7, Key: "ANTHROPIC_BASE_URL", Value: "https://api.deepseek.com/anthropic"},
		},
		accounts: []store.EnvAccount{{Name: "DeepSeek", ApiKey: "sk-real-secret"}},
	}
	rows, err := Resolve(context.Background(), q, 7)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	got := envMap(rows)
	if got["ANTHROPIC_AUTH_TOKEN"] != "sk-real-secret" {
		t.Errorf("account ref not substituted: got %q, want sk-real-secret", got["ANTHROPIC_AUTH_TOKEN"])
	}
	if got["ANTHROPIC_BASE_URL"] != "https://api.deepseek.com/anthropic" {
		t.Errorf("non-ref value mutated: got %q", got["ANTHROPIC_BASE_URL"])
	}
}

func TestResolve_AccountRefMissingKeepsPlaceholder(t *testing.T) {
	q := fakeQuerier{
		env: []store.WorkspaceEnv{
			{WorkspaceID: 7, Key: "ANTHROPIC_AUTH_TOKEN", Value: "${ACCOUNT:NoSuchAccount}"},
		},
		accounts: []store.EnvAccount{{Name: "DeepSeek", ApiKey: "sk-real-secret"}},
	}
	rows, err := Resolve(context.Background(), q, 7)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	// Missing account keeps the literal placeholder so the agent fails loudly.
	if got := envMap(rows)["ANTHROPIC_AUTH_TOKEN"]; got != "${ACCOUNT:NoSuchAccount}" {
		t.Errorf("missing account should keep placeholder, got %q", got)
	}
}

func TestResolve_BoundProviderExpandedNoScene(t *testing.T) {
	// A workspace with a directly-bound provider (workspaces.env_provider_id)
	// gets its env expanded per cli_type with no scene required.
	prov := store.EnvProvider{
		ID: 5, Name: "DeepSeek",
		BaseUrls: `{"anthropic":"https://api.deepseek.com/anthropic"}`, ApiKey: "${ACCOUNT:DeepSeek}",
		Model: "deepseek-v4",
	}
	q := fakeQuerier{
		boundProvider: &prov,
		accounts:      []store.EnvAccount{{Name: "DeepSeek", ApiKey: "sk-real"}},
		cliType:       "claude",
	}
	rows, err := Resolve(context.Background(), q, 7)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	got := envMap(rows)
	if got["ANTHROPIC_BASE_URL"] != "https://api.deepseek.com/anthropic" {
		t.Errorf("bound provider base_url not expanded: %q", got["ANTHROPIC_BASE_URL"])
	}
	if got["ANTHROPIC_AUTH_TOKEN"] != "sk-real" {
		t.Errorf("bound provider account key not substituted: %q", got["ANTHROPIC_AUTH_TOKEN"])
	}
}

func TestResolve_BoundProviderOverriddenByExplicitEnv(t *testing.T) {
	// Explicit workspace_env wins over the bound provider's generated env.
	prov := store.EnvProvider{ID: 5, Name: "DeepSeek",
		BaseUrls: `{"anthropic":"https://api.deepseek.com/anthropic"}`, Model: "deepseek-v4"}
	q := fakeQuerier{
		env:           []store.WorkspaceEnv{{WorkspaceID: 7, Key: "ANTHROPIC_BASE_URL", Value: "https://explicit.override"}},
		boundProvider: &prov,
		cliType:       "claude",
	}
	rows, err := Resolve(context.Background(), q, 7)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if envMap(rows)["ANTHROPIC_BASE_URL"] != "https://explicit.override" {
		t.Errorf("explicit env should win over bound provider: %v", envMap(rows)["ANTHROPIC_BASE_URL"])
	}
}

func TestResolve_SceneProviderExpandedPerCliType(t *testing.T) {
	q := fakeQuerier{
		projection: store.WorkspaceSceneProjection{ProjectedDefinition: `{
			"assets": {"providers": [{"name": "DeepSeek"}]}
		}`},
		providers: []store.EnvProvider{{
			Name: "DeepSeek", BaseUrls: `{"anthropic":"https://api.deepseek.com/anthropic"}`,
			ApiKey: "${ACCOUNT:DeepSeek}", Model: "deepseek-v4", SubagentModel: "deepseek-v4-flash",
		}},
		accounts: []store.EnvAccount{{Name: "DeepSeek", ApiKey: "sk-real"}},
		cliType:  "claude",
	}
	rows, err := Resolve(context.Background(), q, 7)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	got := envMap(rows)
	if got["ANTHROPIC_BASE_URL"] != "https://api.deepseek.com/anthropic" {
		t.Errorf("provider base_url not expanded: %q", got["ANTHROPIC_BASE_URL"])
	}
	if got["ANTHROPIC_AUTH_TOKEN"] != "sk-real" {
		t.Errorf("provider account key not substituted: %q", got["ANTHROPIC_AUTH_TOKEN"])
	}
	if got["ANTHROPIC_MODEL"] != "deepseek-v4" {
		t.Errorf("provider model not expanded: %q", got["ANTHROPIC_MODEL"])
	}
}

func TestResolve_SceneProviderOpenAIProtocol(t *testing.T) {
	q := fakeQuerier{
		projection: store.WorkspaceSceneProjection{ProjectedDefinition: `{
			"assets": {"providers": [{"name": "DeepSeekOpenAI"}]}
		}`},
		providers: []store.EnvProvider{{
			Name: "DeepSeekOpenAI", BaseUrls: `{"openai":"https://api.deepseek.com/v1"}`,
			ApiKey: "${ACCOUNT:DeepSeek}", Model: "deepseek-v4",
		}},
		accounts: []store.EnvAccount{{Name: "DeepSeek", ApiKey: "sk-real"}},
		cliType:  "codex",
	}
	rows, err := Resolve(context.Background(), q, 7)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	got := envMap(rows)
	if got["OPENAI_BASE_URL"] != "https://api.deepseek.com/v1" {
		t.Errorf("openai base_url not expanded: %q", got["OPENAI_BASE_URL"])
	}
	// Codex reads the model from NIUNIU_MODEL for openai-protocol providers.
	if got["NIUNIU_MODEL"] != "deepseek-v4" {
		t.Errorf("codex NIUNIU_MODEL not set: %q", got["NIUNIU_MODEL"])
	}
	if _, ok := got["ANTHROPIC_MODEL"]; ok {
		t.Error("openai provider should not emit ANTHROPIC_MODEL")
	}
}

func TestSubstituteAccounts_SceneValueSubstituted(t *testing.T) {
	accounts := []store.EnvAccount{{Name: "智谱", ApiKey: "zhipu-key"}}
	rows := []store.WorkspaceEnv{
		{Key: "ANTHROPIC_AUTH_TOKEN", Value: "${ACCOUNT:智谱}"},
		{Key: "PLAIN", Value: "not-a-ref"},
	}
	out := SubstituteAccounts(accounts, rows)
	if out[0].Value != "zhipu-key" {
		t.Errorf("scene account ref not substituted: got %q", out[0].Value)
	}
	if out[1].Value != "not-a-ref" {
		t.Errorf("non-ref value mutated: got %q", out[1].Value)
	}
	// Input slice must not be mutated.
	if rows[0].Value != "${ACCOUNT:智谱}" {
		t.Error("input slice was mutated")
	}
}
