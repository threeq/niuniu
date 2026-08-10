package service

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setWorkspaceCliType flips a workspace's cli_type so the projector resolves a
// non-Claude materialization target.
func setWorkspaceCliType(t *testing.T, db *sql.DB, wsID int64, cliType string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`UPDATE workspaces SET cli_type = ? WHERE id = ?`, cliType, wsID)
	require.NoError(t, err)
}

func TestSceneProjector_MaterializesAgent_Qwen(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws := createTestWorkspace(t, db, dataDir)
	setWorkspaceCliType(t, db, ws.ID, "qwen")
	svc := NewSceneLayerService(db, makeTestProjector(t, db, dataDir))
	sceneSvc := NewSceneService(db)

	agentFile := filepath.Join(dataDir, "agents", "scene-reviewer.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(agentFile), 0o755))
	require.NoError(t, os.WriteFile(agentFile,
		[]byte("---\nname: scene-reviewer\ndescription: Reviews output\n---\nReview against the active scene.\n"), 0o644))
	_, err := db.ExecContext(ctx,
		`INSERT INTO agents (name, description, dir_path, file_hash, owner_type, owner_id) VALUES (?, ?, ?, '', 'user', 1)`,
		"scene-reviewer", "Reviews output", agentFile)
	require.NoError(t, err)

	scene, err := sceneSvc.Create(ctx, OwnerRef{Type: "user", ID: 1}, "qwen-scene", "Qwen Scene", "", nil, &SceneDefinition{
		Assets: SceneAssets{Agents: []AgentAsset{{Name: "scene-reviewer"}}},
	})
	require.NoError(t, err)
	_, err = svc.Attach(ctx, ws.ID, scene.ID, nil)
	require.NoError(t, err)

	wsDir := OwnerRef{Type: "user", ID: 1}.WorkspacePath(dataDir, ws.ID)
	// Lands under .qwen/agents as markdown, NOT .claude/agents.
	materialized := filepath.Join(wsDir, ".qwen", "agents", "scene-reviewer.md")
	body, err := os.ReadFile(materialized)
	require.NoError(t, err)
	assert.Contains(t, string(body), "Review against the active scene.")
	assert.Contains(t, string(body), "managed_by: niuniu")
	assert.NoFileExists(t, filepath.Join(wsDir, ".claude", "agents", "scene-reviewer.md"))
}

func TestSceneProjector_MaterializesAgent_Codex(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws := createTestWorkspace(t, db, dataDir)
	setWorkspaceCliType(t, db, ws.ID, "codex")
	svc := NewSceneLayerService(db, makeTestProjector(t, db, dataDir))
	sceneSvc := NewSceneService(db)

	agentFile := filepath.Join(dataDir, "agents", "scene-reviewer.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(agentFile), 0o755))
	require.NoError(t, os.WriteFile(agentFile,
		[]byte("---\nname: scene-reviewer\ndescription: fm-desc\nmodel: gpt-5\n---\nReview against the active scene.\n"), 0o644))
	_, err := db.ExecContext(ctx,
		`INSERT INTO agents (name, description, dir_path, file_hash, owner_type, owner_id) VALUES (?, ?, ?, '', 'user', 1)`,
		"scene-reviewer", "Reviews output", agentFile)
	require.NoError(t, err)

	scene, err := sceneSvc.Create(ctx, OwnerRef{Type: "user", ID: 1}, "codex-scene", "Codex Scene", "", nil, &SceneDefinition{
		Assets: SceneAssets{Agents: []AgentAsset{{Name: "scene-reviewer"}}},
	})
	require.NoError(t, err)
	_, err = svc.Attach(ctx, ws.ID, scene.ID, nil)
	require.NoError(t, err)

	wsDir := OwnerRef{Type: "user", ID: 1}.WorkspacePath(dataDir, ws.ID)
	// Lands under .codex/agents as TOML, NOT .claude/agents.
	materialized := filepath.Join(wsDir, ".codex", "agents", "scene-reviewer.toml")
	raw, err := os.ReadFile(materialized)
	require.NoError(t, err)

	var doc codexDoc
	require.NoError(t, toml.Unmarshal(raw, &doc), "materialized codex agent must be valid TOML")
	assert.Equal(t, "niuniu", doc.ManagedBy)
	assert.Equal(t, "scene-reviewer", doc.Name)
	assert.Equal(t, "Reviews output", doc.Description) // DB description wins
	assert.Equal(t, "gpt-5", doc.Model)
	assert.Equal(t, "Review against the active scene.\n", doc.DeveloperInstructions)
	assert.NoFileExists(t, filepath.Join(wsDir, ".claude", "agents", "scene-reviewer.md"))
}

// Switching a workspace's CLI must clear the previous CLI's niuniu-managed
// agent files while leaving user-installed agents intact.
func TestSceneProjector_CliSwitch_CleansStaleManagedAgents(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws := createTestWorkspace(t, db, dataDir)
	svc := NewSceneLayerService(db, makeTestProjector(t, db, dataDir))
	sceneSvc := NewSceneService(db)

	agentFile := filepath.Join(dataDir, "agents", "scene-reviewer.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(agentFile), 0o755))
	require.NoError(t, os.WriteFile(agentFile,
		[]byte("---\nname: scene-reviewer\ndescription: d\n---\nbody\n"), 0o644))
	_, err := db.ExecContext(ctx,
		`INSERT INTO agents (name, description, dir_path, file_hash, owner_type, owner_id) VALUES (?, ?, ?, '', 'user', 1)`,
		"scene-reviewer", "d", agentFile)
	require.NoError(t, err)

	scene, err := sceneSvc.Create(ctx, OwnerRef{Type: "user", ID: 1}, "s", "S", "", nil, &SceneDefinition{
		Assets: SceneAssets{Agents: []AgentAsset{{Name: "scene-reviewer"}}},
	})
	require.NoError(t, err)
	_, err = svc.Attach(ctx, ws.ID, scene.ID, nil)
	require.NoError(t, err)

	wsDir := OwnerRef{Type: "user", ID: 1}.WorkspacePath(dataDir, ws.ID)
	claudeFile := filepath.Join(wsDir, ".claude", "agents", "scene-reviewer.md")
	require.FileExists(t, claudeFile)

	// A user-installed (non-managed) agent in the same dir must survive cleanup.
	userFile := filepath.Join(wsDir, ".claude", "agents", "my-own.md")
	require.NoError(t, os.WriteFile(userFile, []byte("---\nname: my-own\n---\nmine\n"), 0o644))

	// Switch the workspace to codex and recompute.
	setWorkspaceCliType(t, db, ws.ID, "codex")
	_, err = svc.projector.Apply(ctx, ws.ID)
	require.NoError(t, err)

	// The stale Claude-managed agent is gone; the user agent is preserved; the
	// codex agent now exists.
	assert.NoFileExists(t, claudeFile)
	assert.FileExists(t, userFile)
	assert.FileExists(t, filepath.Join(wsDir, ".codex", "agents", "scene-reviewer.toml"))
}
