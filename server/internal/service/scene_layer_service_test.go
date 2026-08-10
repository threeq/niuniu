package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/harness"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// makeTestProjector builds a projector that skips MCP+plugin side effects
// (both nil) so unit tests don't need a real claude binary on PATH or a
// mcpGen instance. CLAUDE.md splicing and asset imports still run.
func makeTestProjector(t *testing.T, db *sql.DB, dataDir string) *SceneProjector {
	t.Helper()
	return NewSceneProjector(db, dataDir, nil, nil, nil, nil, nil)
}

func createTestWorkspace(t *testing.T, db *sql.DB, dataDir string) store.Workspace {
	t.Helper()
	q := store.New(db)
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name:      "ws-test",
		Path:      filepath.Join(dataDir, "ws-tmp"),
		Status:    "created",
		OwnerType: "user",
		OwnerID:   1,
		CreatedBy: sql.NullInt64{Int64: 1, Valid: true},
	})
	require.NoError(t, err)
	owner := OwnerRef{Type: "user", ID: 1}
	wsDir := owner.WorkspacePath(dataDir, ws.ID)
	require.NoError(t, os.MkdirAll(wsDir, 0o755))
	updated, err := q.UpdateWorkspacePath(context.Background(), store.UpdateWorkspacePathParams{
		ID:   ws.ID,
		Path: wsDir,
	})
	require.NoError(t, err)
	return updated
}

func TestSceneLayerService_EnsureBaseLayerIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws := createTestWorkspace(t, db, dataDir)
	svc := NewSceneLayerService(db, makeTestProjector(t, db, dataDir))

	base1, err := svc.EnsureBaseLayer(ctx, ws.ID)
	require.NoError(t, err)
	base2, err := svc.EnsureBaseLayer(ctx, ws.ID)
	require.NoError(t, err)
	assert.Equal(t, base1.ID, base2.ID, "second EnsureBaseLayer must return same row")
	assert.Equal(t, int64(1), base1.IsBase)
}

func TestSceneLayerService_AttachComputesProjection(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws := createTestWorkspace(t, db, dataDir)
	svc := NewSceneLayerService(db, makeTestProjector(t, db, dataDir))

	// Seed a scene with one MCP + one prompt.
	sceneSvc := NewSceneService(db)
	scene, err := sceneSvc.Create(ctx, OwnerRef{Type: "user", ID: 1}, "demo", "Demo", "", nil, &SceneDefinition{
		MCP:     []MCPDecl{{Name: "memory"}},
		Prompts: []PromptFragment{{ID: "rule-1", Title: "Rule 1", Body: "Be polite."}},
	})
	require.NoError(t, err)

	got, err := svc.Attach(ctx, ws.ID, scene.ID, nil)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, []string{"memory"}, got.Projection.MCPNames)
	require.Len(t, got.Projection.Prompts, 1)

	// CLAUDE.md got the fragment.
	owner := OwnerRef{Type: "user", ID: 1}
	wsDir := owner.WorkspacePath(dataDir, ws.ID)
	md, err := os.ReadFile(filepath.Join(wsDir, "CLAUDE.md"))
	require.NoError(t, err)
	assert.Contains(t, string(md), "Rule 1")
	assert.Contains(t, string(md), ClaudeMdFragmentBegin)

	// Projection cache row present.
	proj, err := store.New(db).GetProjection(ctx, ws.ID)
	require.NoError(t, err)
	assert.Equal(t, got.Digest, proj.Digest)
}

func TestSceneLayerService_AttachWritesAgentsMdForCodexWorkspace(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws := createTestWorkspace(t, db, dataDir)
	_, err := db.ExecContext(ctx, `UPDATE workspaces SET cli_type = 'codex' WHERE id = ?`, ws.ID)
	require.NoError(t, err)

	svc := NewSceneLayerService(db, makeTestProjector(t, db, dataDir))
	sceneSvc := NewSceneService(db)
	scene, err := sceneSvc.Create(ctx, OwnerRef{Type: "user", ID: 1}, "codex-demo", "Codex Demo", "", nil, &SceneDefinition{
		Prompts: []PromptFragment{{ID: "codex-rule", Title: "Codex Rule", Body: "Use the scene guidance."}},
	})
	require.NoError(t, err)

	_, err = svc.Attach(ctx, ws.ID, scene.ID, nil)
	require.NoError(t, err)

	owner := OwnerRef{Type: "user", ID: 1}
	wsDir := owner.WorkspacePath(dataDir, ws.ID)
	agentsMd, err := os.ReadFile(filepath.Join(wsDir, "AGENTS.md"))
	require.NoError(t, err)
	assert.Contains(t, string(agentsMd), "Codex Rule")
	assert.Contains(t, string(agentsMd), ClaudeMdFragmentBegin)

	claudeMd, err := os.ReadFile(filepath.Join(wsDir, "CLAUDE.md"))
	require.NoError(t, err)
	assert.Contains(t, string(claudeMd), "Codex Rule")
}

func TestSceneLayerService_DetachRemovesFragment(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws := createTestWorkspace(t, db, dataDir)
	svc := NewSceneLayerService(db, makeTestProjector(t, db, dataDir))
	sceneSvc := NewSceneService(db)

	scene, err := sceneSvc.Create(ctx, OwnerRef{Type: "user", ID: 1}, "demo", "Demo", "", nil, &SceneDefinition{
		Prompts: []PromptFragment{{ID: "rule-1", Title: "Rule 1", Body: "be polite"}},
	})
	require.NoError(t, err)
	res, err := svc.Attach(ctx, ws.ID, scene.ID, nil)
	require.NoError(t, err)

	// Find the layer id (base + 1 attached).
	layers, err := svc.List(ctx, ws.ID)
	require.NoError(t, err)
	var attachedID int64
	for _, l := range layers {
		if l.IsBase == 0 {
			attachedID = l.ID
		}
	}
	require.NotZero(t, attachedID)

	res, err = svc.Detach(ctx, ws.ID, attachedID)
	require.NoError(t, err)
	assert.Empty(t, res.Projection.Prompts)

	owner := OwnerRef{Type: "user", ID: 1}
	wsDir := owner.WorkspacePath(dataDir, ws.ID)
	md, _ := os.ReadFile(filepath.Join(wsDir, "CLAUDE.md"))
	assert.NotContains(t, string(md), ClaudeMdFragmentBegin)
}

func TestSceneProjector_RecomputeRespectsLayerOrder(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws := createTestWorkspace(t, db, dataDir)
	svc := NewSceneLayerService(db, makeTestProjector(t, db, dataDir))
	sceneSvc := NewSceneService(db)

	// Base layer with MCP "alpha".
	require.NoError(t, svc.SaveBaseDefinition(ctx, ws.ID, `{"mcp":[{"name":"alpha"}]}`))

	// Scene with MCP "beta".
	scene, err := sceneSvc.Create(ctx, OwnerRef{Type: "user", ID: 1}, "demo", "Demo", "", nil, &SceneDefinition{
		MCP: []MCPDecl{{Name: "beta"}},
	})
	require.NoError(t, err)
	_, err = svc.Attach(ctx, ws.ID, scene.ID, nil)
	require.NoError(t, err)

	proj, err := svc.projector.Recompute(ctx, ws.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "beta"}, proj.MCPNames)
}

func TestSceneProjector_ImportsEnvPresetButQuickActionStaysInProjection(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws := createTestWorkspace(t, db, dataDir)
	svc := NewSceneLayerService(db, makeTestProjector(t, db, dataDir))
	sceneSvc := NewSceneService(db)

	scene, err := sceneSvc.Create(ctx, OwnerRef{Type: "user", ID: 1}, "demo", "Demo", "", nil, &SceneDefinition{
		Assets: SceneAssets{
			EnvPresets: []EnvPresetAsset{{
				Slug: "demo-env", Name: "Demo Env", Env: map[string]string{"K": "V"},
			}},
			QuickActions: []QuickActionAsset{{
				Slug: "demo-qa", Label: "Demo QA", Prompt: "do it",
			}},
		},
	})
	require.NoError(t, err)
	_, err = svc.Attach(ctx, ws.ID, scene.ID, nil)
	require.NoError(t, err)

	// Verify env_presets row.
	q := store.New(db)
	presets, err := q.ListEnvPresets(ctx)
	require.NoError(t, err)
	var foundEP bool
	for _, p := range presets {
		if p.Slug == "demo-env" {
			foundEP = true
			var env map[string]string
			require.NoError(t, json.Unmarshal([]byte(p.Env), &env))
			assert.Equal(t, "V", env["K"])
		}
	}
	assert.True(t, foundEP, "env_preset should be imported")

	// Quick actions are NOT persisted to the quick_actions table — they are
	// surfaced live from the projection as a separate, read-only group.
	var qaCount int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM quick_actions WHERE owner_type='user' AND owner_id=1 AND slug='demo-qa'`).Scan(&qaCount))
	assert.Equal(t, 0, qaCount, "scene quick_action must NOT be persisted")

	// ...but it IS available live from the workspace projection.
	qaSvc := NewQuickActionService(store.New(db), db, nil)
	sceneQAs, err := qaSvc.ListSceneQuickActionsForWorkspace(ctx, ws.ID)
	require.NoError(t, err)
	var foundQA bool
	for _, qa := range sceneQAs {
		if qa.Slug == "demo-qa" {
			foundQA = true
			assert.Equal(t, "Demo QA", qa.Label)
			assert.Equal(t, "do it", qa.Content)
		}
	}
	assert.True(t, foundQA, "scene quick_action should be available from the projection")

	// Re-applying must not double-insert.
	_, err = svc.projector.Apply(ctx, ws.ID)
	require.NoError(t, err)
	var n int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM env_presets WHERE slug='demo-env'`).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestSceneProjector_ImportsHarnessSpecAndMaterializesAgent(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws := createTestWorkspace(t, db, dataDir)
	svc := NewSceneLayerService(db, makeTestProjector(t, db, dataDir))
	sceneSvc := NewSceneService(db)

	// Pre-create the referenced agent (as if added on the Agents page). Scenes
	// reference agents by name — they are NOT authored inline.
	agentFile := filepath.Join(dataDir, "agents", "scene-reviewer.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(agentFile), 0o755))
	require.NoError(t, os.WriteFile(agentFile,
		[]byte("---\nname: scene-reviewer\ndescription: Reviews scene output\n---\nReview the work against the active scene.\n"), 0o644))
	_, err := db.ExecContext(ctx,
		`INSERT INTO agents (name, description, dir_path, file_hash, owner_type, owner_id) VALUES (?, ?, ?, '', 'user', 1)`,
		"scene-reviewer", "Reviews scene output", agentFile)
	require.NoError(t, err)

	scene, err := sceneSvc.Create(ctx, OwnerRef{Type: "user", ID: 1}, "capability-assets", "Capability Assets", "", nil, &SceneDefinition{
		Assets: SceneAssets{
			HarnessSpecs: []HarnessSpecAsset{{
				Slug: "scene-precommit",
				Name: "Scene Precommit",
				Payload: map[string]any{
					"category":    "commit",
					"severity":    "error",
					"kind":        "regex_match",
					"target":      "commit_message",
					"pattern":     ".+",
					"trigger_on":  "pre_commit",
					"timeout_sec": float64(30),
				},
			}},
			Agents: []AgentAsset{{Name: "scene-reviewer"}},
		},
	})
	require.NoError(t, err)

	_, err = svc.Attach(ctx, ws.ID, scene.ID, nil)
	require.NoError(t, err)

	// Harness spec import is unchanged.
	var specID int64
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT id FROM harness_specs WHERE name='Scene Precommit' AND trigger_on='pre_commit'`).Scan(&specID))
	require.NotZero(t, specID)
	harnessSvc := NewHarnessService(store.New(db), nil)
	specs, err := harnessSvc.ResolveForProject(ctx, nil)
	require.NoError(t, err)
	foundSpec := false
	for _, spec := range specs {
		if spec.ID == specID && spec.Enabled && spec.TriggerOn == harness.TriggerPreCommit {
			foundSpec = true
			break
		}
	}
	assert.True(t, foundSpec, "scene-imported harness spec should be visible to harness resolution")

	// Agent is materialized into the workspace's Claude subagent dir (stamped
	// managed_by: niuniu), NOT inserted as a new agents row.
	wsDir := OwnerRef{Type: "user", ID: 1}.WorkspacePath(dataDir, ws.ID)
	materialized := filepath.Join(wsDir, ".claude", "agents", "scene-reviewer.md")
	body, err := os.ReadFile(materialized)
	require.NoError(t, err)
	assert.Contains(t, string(body), "Review the work against the active scene.")
	assert.Contains(t, string(body), "managed_by: niuniu")

	// Re-applying is idempotent and creates no extra agent rows.
	_, err = svc.projector.Apply(ctx, ws.ID)
	require.NoError(t, err)
	var n int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agents WHERE owner_type='user' AND owner_id=1 AND name='scene-reviewer'`).Scan(&n))
	assert.Equal(t, 1, n, "materialization must not create new agent rows")
}

func TestSceneProjector_RestartRequiredOnMCPChange(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws := createTestWorkspace(t, db, dataDir)
	svc := NewSceneLayerService(db, makeTestProjector(t, db, dataDir))
	sceneSvc := NewSceneService(db)

	sceneA, err := sceneSvc.Create(ctx, OwnerRef{Type: "user", ID: 1}, "a", "A", "", nil, &SceneDefinition{
		MCP: []MCPDecl{{Name: "memory"}},
	})
	require.NoError(t, err)
	res1, err := svc.Attach(ctx, ws.ID, sceneA.ID, nil)
	require.NoError(t, err)
	assert.False(t, res1.RestartRequired, "first attach: no prior projection → no restart")

	sceneB, err := sceneSvc.Create(ctx, OwnerRef{Type: "user", ID: 1}, "b", "B", "", nil, &SceneDefinition{
		MCP: []MCPDecl{{Name: "context7"}},
	})
	require.NoError(t, err)
	res2, err := svc.Attach(ctx, ws.ID, sceneB.ID, nil)
	require.NoError(t, err)
	assert.True(t, res2.RestartRequired, "MCP set changed → restart required")
}

func TestSceneProjector_MissingCredentials(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws := createTestWorkspace(t, db, dataDir)
	svc := NewSceneLayerService(db, makeTestProjector(t, db, dataDir))
	sceneSvc := NewSceneService(db)

	scene, err := sceneSvc.Create(ctx, OwnerRef{Type: "user", ID: 1}, "needs-cred", "Needs", "", nil, &SceneDefinition{
		RequiredCredentials: []RequiredCredential{
			{Alias: "main", Provider: "github", Optional: false},
		},
	})
	require.NoError(t, err)
	res, err := svc.Attach(ctx, ws.ID, scene.ID, nil)
	require.NoError(t, err)
	require.Len(t, res.MissingCredentials, 1)
	assert.Equal(t, "github", res.MissingCredentials[0].Provider)
}

// Verify the embedded fragment is removed cleanly (no orphan markers) when
// the prompt-bearing scene is detached.
func TestSceneProjector_ClaudeMdFragmentClean(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws := createTestWorkspace(t, db, dataDir)
	svc := NewSceneLayerService(db, makeTestProjector(t, db, dataDir))
	sceneSvc := NewSceneService(db)

	owner := OwnerRef{Type: "user", ID: 1}
	wsDir := owner.WorkspacePath(dataDir, ws.ID)
	// Pre-existing CLAUDE.md content that the projector must preserve.
	require.NoError(t, os.WriteFile(filepath.Join(wsDir, "CLAUDE.md"),
		[]byte("# Workspace\n\nUser-authored body.\n"), 0o644))

	scene, err := sceneSvc.Create(ctx, owner, "p", "P", "", nil, &SceneDefinition{
		Prompts: []PromptFragment{{ID: "x", Title: "T", Body: "B"}},
	})
	require.NoError(t, err)
	_, err = svc.Attach(ctx, ws.ID, scene.ID, nil)
	require.NoError(t, err)

	md, err := os.ReadFile(filepath.Join(wsDir, "CLAUDE.md"))
	require.NoError(t, err)
	assert.Contains(t, string(md), "User-authored body.")
	assert.Contains(t, string(md), ClaudeMdFragmentBegin)

	layers, _ := svc.List(ctx, ws.ID)
	var attachedID int64
	for _, l := range layers {
		if l.IsBase == 0 {
			attachedID = l.ID
		}
	}
	_, err = svc.Detach(ctx, ws.ID, attachedID)
	require.NoError(t, err)

	md, err = os.ReadFile(filepath.Join(wsDir, "CLAUDE.md"))
	require.NoError(t, err)
	assert.Contains(t, string(md), "User-authored body.")
	assert.NotContains(t, string(md), ClaudeMdFragmentBegin)
	assert.NotContains(t, string(md), ClaudeMdFragmentEnd)
	// Sanity: trailing markers should be cleaned, no double-newline glitches
	// that would surface as visible whitespace runs.
	assert.False(t, strings.Contains(string(md), "\n\n\n\n"))
}
