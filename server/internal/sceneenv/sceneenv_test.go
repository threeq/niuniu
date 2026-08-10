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
	env        []store.WorkspaceEnv
	envErr     error
	projection store.WorkspaceSceneProjection
	projErr    error
}

func (f fakeQuerier) ListWorkspaceEnv(_ context.Context, _ int64) ([]store.WorkspaceEnv, error) {
	return f.env, f.envErr
}

func (f fakeQuerier) GetProjection(_ context.Context, _ int64) (store.WorkspaceSceneProjection, error) {
	return f.projection, f.projErr
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
