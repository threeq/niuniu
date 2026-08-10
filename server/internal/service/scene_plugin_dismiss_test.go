package service

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSetPluginDismissed_PersistsAndSurvivesRecompute verifies the escape
// hatch for un-installable plugins: dismissing a source persists it, surfaces
// it in ApplyResult.DismissedPlugins, survives a subsequent recompute (Apply),
// and is reversible.
func TestSetPluginDismissed_PersistsAndSurvivesRecompute(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws := createTestWorkspace(t, db, dataDir)
	projector := makeTestProjector(t, db, dataDir)
	svc := NewSceneLayerService(db, projector)

	// Attach a scene that declares a plugin with a (deliberately) wrong
	// marketplace — the exact shape that produces a sticky install banner.
	sceneSvc := NewSceneService(db)
	scene, err := sceneSvc.Create(ctx, OwnerRef{Type: "user", ID: 1}, "demo", "Demo", "", nil, &SceneDefinition{
		Plugins: []PluginDecl{{Source: "document-skills@claude-plugins-official"}},
	})
	require.NoError(t, err)
	_, err = svc.Attach(ctx, ws.ID, scene.ID, nil)
	require.NoError(t, err)

	// Dismiss the plugin.
	res, err := projector.SetPluginDismissed(ctx, ws.ID, "document-skills@claude-plugins-official", true)
	require.NoError(t, err)
	assert.Equal(t, []string{"document-skills@claude-plugins-official"}, res.DismissedPlugins)

	// Persisted in its own column.
	row, err := store.New(db).GetProjection(ctx, ws.ID)
	require.NoError(t, err)
	assert.Equal(t, `["document-skills@claude-plugins-official"]`, row.DismissedPlugins)

	// Survives a recompute — Apply must not clobber dismissed_plugins.
	after, err := projector.Apply(ctx, ws.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"document-skills@claude-plugins-official"}, after.DismissedPlugins)

	// Restore brings it back (empty dismissed set).
	restored, err := projector.SetPluginDismissed(ctx, ws.ID, "document-skills@claude-plugins-official", false)
	require.NoError(t, err)
	assert.Empty(t, restored.DismissedPlugins)
	row, err = store.New(db).GetProjection(ctx, ws.ID)
	require.NoError(t, err)
	assert.Equal(t, `[]`, row.DismissedPlugins)
}

func TestSetPluginDismissed_RejectsEmptySource(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws := createTestWorkspace(t, db, dataDir)
	projector := makeTestProjector(t, db, dataDir)

	_, err := projector.SetPluginDismissed(ctx, ws.ID, "   ", true)
	require.Error(t, err)
}

func TestFilterDismissedResults(t *testing.T) {
	rows := []PluginInstallResult{
		{Source: "a@mp", Status: PluginInstallStatusPending},
		{Source: "b@mp", Status: PluginInstallStatusFailed},
		{Source: "c@mp", Status: PluginInstallStatusPending},
	}
	got := filterDismissedResults(rows, []string{"b@mp"})
	require.Len(t, got, 2)
	for _, r := range got {
		assert.NotEqual(t, "b@mp", r.Source)
	}

	// Nothing dismissed → input returned unchanged.
	assert.Len(t, filterDismissedResults(rows, nil), 3)
}

func TestDecodeDismissedPlugins(t *testing.T) {
	assert.Nil(t, decodeDismissedPlugins(""))
	assert.Nil(t, decodeDismissedPlugins("not json"))
	assert.Equal(t, []string{"x@mp"}, decodeDismissedPlugins(`["x@mp"]`))
}
