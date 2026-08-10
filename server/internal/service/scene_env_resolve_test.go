package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/sceneenv"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// fakeSceneEnvQuerier feeds a real, marshaled Projection through sceneenv.Resolve
// so this test locks the contract between what SceneProjector.Apply persists and
// what sceneenv decodes. If the Projection JSON shape ever drifts from
// sceneenv's decoder, this test fails.
type fakeSceneEnvQuerier struct {
	env  []store.WorkspaceEnv
	proj store.WorkspaceSceneProjection
}

func (f fakeSceneEnvQuerier) ListWorkspaceEnv(context.Context, int64) ([]store.WorkspaceEnv, error) {
	return f.env, nil
}

func (f fakeSceneEnvQuerier) GetProjection(context.Context, int64) (store.WorkspaceSceneProjection, error) {
	return f.proj, nil
}

func TestSceneEnvResolve_DecodesRealProjectionJSON(t *testing.T) {
	// Build a Projection exactly as Recompute would: fold a scene definition
	// that declares an env_preset asset.
	proj := NewProjection()
	proj.MergeFrom(&SceneDefinition{
		Assets: SceneAssets{
			EnvPresets: []EnvPresetAsset{{
				Slug: "provider",
				Env: map[string]string{
					"ANTHROPIC_BASE_URL":   "https://scene.example",
					"ANTHROPIC_AUTH_TOKEN": "scene-token",
				},
			}},
		},
	}, LayerOrigin(1))

	// Persist exactly as Apply step 7 does.
	body, err := json.Marshal(proj)
	if err != nil {
		t.Fatalf("marshal projection: %v", err)
	}

	q := fakeSceneEnvQuerier{
		env:  []store.WorkspaceEnv{{WorkspaceID: 1, Key: "NIUNIU_PERMISSION_MODE", Value: "autohost"}},
		proj: store.WorkspaceSceneProjection{ProjectedDefinition: string(body)},
	}

	rows, err := sceneenv.Resolve(context.Background(), q, 1)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := map[string]string{}
	for _, r := range rows {
		got[r.Key] = r.Value
	}
	if got["ANTHROPIC_BASE_URL"] != "https://scene.example" {
		t.Errorf("scene env not decoded from real Projection JSON: ANTHROPIC_BASE_URL=%q", got["ANTHROPIC_BASE_URL"])
	}
	if got["ANTHROPIC_AUTH_TOKEN"] != "scene-token" {
		t.Errorf("scene env not decoded from real Projection JSON: ANTHROPIC_AUTH_TOKEN=%q", got["ANTHROPIC_AUTH_TOKEN"])
	}
	if got["NIUNIU_PERMISSION_MODE"] != "autohost" {
		t.Errorf("explicit workspace_env lost: NIUNIU_PERMISSION_MODE=%q", got["NIUNIU_PERMISSION_MODE"])
	}
}

// TestSceneEnvIntegration_AttachThenResolve drives the full path end to end:
// create a scene that declares env vars, attach it to a workspace (which runs
// the real SceneProjector.Apply and persists the projection cache), then
// resolve the workspace env the way every agent spawn path now does. It proves
// the scene's env vars are auto-injected and accessible after the workspace's
// scene stack is established, and that an explicitly-set workspace var still
// wins over the scene's value for the same key.
func TestSceneEnvIntegration_AttachThenResolve(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws := createTestWorkspace(t, db, dataDir)
	q := store.New(db)

	// An explicit workspace var the scene also tries to set — must win.
	if err := q.SetWorkspaceEnv(ctx, store.SetWorkspaceEnvParams{
		WorkspaceID: ws.ID,
		Key:         "ANTHROPIC_AUTH_TOKEN",
		Value:       "user-token",
	}); err != nil {
		t.Fatalf("seed workspace env: %v", err)
	}

	sceneSvc := NewSceneService(db)
	scene, err := sceneSvc.Create(ctx, OwnerRef{Type: "user", ID: 1}, "provider", "Provider", "", nil, &SceneDefinition{
		Assets: SceneAssets{
			EnvPresets: []EnvPresetAsset{{
				Slug: "provider",
				Env: map[string]string{
					"ANTHROPIC_BASE_URL":   "https://scene.example",
					"ANTHROPIC_AUTH_TOKEN": "scene-token",
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("create scene: %v", err)
	}

	layers := NewSceneLayerService(db, makeTestProjector(t, db, dataDir))
	if _, err := layers.Attach(ctx, ws.ID, scene.ID, nil); err != nil {
		t.Fatalf("attach scene: %v", err)
	}

	rows, err := sceneenv.Resolve(ctx, q, ws.ID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := map[string]string{}
	for _, r := range rows {
		got[r.Key] = r.Value
	}
	if got["ANTHROPIC_BASE_URL"] != "https://scene.example" {
		t.Errorf("scene env not auto-injected after attach: ANTHROPIC_BASE_URL=%q", got["ANTHROPIC_BASE_URL"])
	}
	if got["ANTHROPIC_AUTH_TOKEN"] != "user-token" {
		t.Errorf("explicit workspace var must win over scene: ANTHROPIC_AUTH_TOKEN=%q", got["ANTHROPIC_AUTH_TOKEN"])
	}
}
