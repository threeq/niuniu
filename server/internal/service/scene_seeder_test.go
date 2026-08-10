package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

const sampleYAML = `slug: test-scene
display_name: Test Scene
description: a unit-test seed
tags: [test, sample]

mcp:
  - name: memory
  - name: context7

plugins:
  - source: github:anthropics/test-plugin
    optional: true

assets:
  env_presets: []
  quick_actions:
    - slug: do-something
      label: 做点事
      prompt: please do
  project_templates: []
  harness_specs: []
  agents: []

prompts:
  - id: rule-x
    title: Rule X
    body: |
      Do not violate Rule X.

required_credentials: []

match:
  base_weight: 5
  rules:
    - signal: workspace.has_repo_count
      args: { min: 1 }
      weight: 10
`

const minimalYAML = `slug: minimal-scene
display_name: Minimal
`

const invalidYAML = `slug: bad-scene
display_name: Bad
plugins:
  - source: evil://nope
`

// TestBuiltinScenes_InfoRadarShape asserts the info-radar builtin scene ships
// embedded, references the info-radar skill, and — critically — does NOT disable
// the multi-agent tool group (the radar needs blackboard dedup + inbox push).
func TestBuiltinScenes_InfoRadarShape(t *testing.T) {
	b, err := builtinScenesFS.ReadFile("builtin_scenes/info-radar.yaml")
	require.NoError(t, err)
	y := string(b)
	assert.Contains(t, y, "skills:")
	assert.Contains(t, y, "info-radar")
	// Only the harness group may be disabled — multi-agent (blackboard dedup +
	// inbox push) MUST stay enabled for the radar to work.
	assert.Contains(t, y, "disable_tool_groups: [harness]")
	// The privacy-sensitive browser-history group is opt-in; this scene turns it
	// on so read_browser_history is available (路子 A).
	assert.Contains(t, y, "enable_tool_groups: [browser-history]")
}

func TestSceneSeeder_RunUpsertsEverything(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	q := store.New(db)
	fsys := fstest.MapFS{
		"test-scene.yaml":    {Data: []byte(sampleYAML)},
		"minimal-scene.yaml": {Data: []byte(minimalYAML)},
		"readme.md":          {Data: []byte("ignore me")},
	}
	seeder := NewSceneSeederFromFS(q, fsys, ".")
	require.NoError(t, seeder.Run(ctx))

	gotA, err := q.GetSceneByOwnerSlug(ctx, store.GetSceneByOwnerSlugParams{
		OwnerType: "user", OwnerID: 0, Slug: "test-scene",
	})
	require.NoError(t, err)
	assert.Equal(t, "Test Scene", gotA.DisplayName)
	assert.Equal(t, "builtin", gotA.Source)

	var def SceneDefinition
	require.NoError(t, json.Unmarshal([]byte(gotA.Definition), &def))
	require.Len(t, def.MCP, 2)
	assert.Equal(t, "memory", def.MCP[0].Name)
	require.Len(t, def.Plugins, 1)
	assert.Equal(t, "github:anthropics/test-plugin", def.Plugins[0].Source)
	assert.True(t, def.Plugins[0].Optional)
	require.Len(t, def.Prompts, 1)
	assert.Equal(t, "rule-x", def.Prompts[0].ID)
	assert.Equal(t, 5, def.Match.BaseWeight)
	require.Len(t, def.Assets.QuickActions, 1)
	assert.Equal(t, "do-something", def.Assets.QuickActions[0].Slug)

	gotB, err := q.GetSceneByOwnerSlug(ctx, store.GetSceneByOwnerSlugParams{
		OwnerType: "user", OwnerID: 0, Slug: "minimal-scene",
	})
	require.NoError(t, err)
	assert.Equal(t, "Minimal", gotB.DisplayName)
}

func TestSceneSeeder_SkipsInvalidYAMLButKeepsRunning(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	q := store.New(db)
	fsys := fstest.MapFS{
		"good.yaml": {Data: []byte(sampleYAML)},
		"bad.yaml":  {Data: []byte(invalidYAML)},
	}
	seeder := NewSceneSeederFromFS(q, fsys, ".")
	require.NoError(t, seeder.Run(ctx))

	got, err := q.GetSceneByOwnerSlug(ctx, store.GetSceneByOwnerSlugParams{
		OwnerType: "user", OwnerID: 0, Slug: "test-scene",
	})
	require.NoError(t, err)
	assert.Equal(t, "Test Scene", got.DisplayName)

	// Bad file must NOT have been inserted.
	_, err = q.GetSceneByOwnerSlug(ctx, store.GetSceneByOwnerSlugParams{
		OwnerType: "user", OwnerID: 0, Slug: "bad-scene",
	})
	assert.Error(t, err)
}

func TestSceneSeeder_UpdatesExistingBuiltinOnReseed(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	q := store.New(db)
	fsys := fstest.MapFS{"x.yaml": {Data: []byte(sampleYAML)}}
	seeder := NewSceneSeederFromFS(q, fsys, ".")
	require.NoError(t, seeder.Run(ctx))

	// Second run with updated payload.
	fsys["x.yaml"] = &fstest.MapFile{Data: []byte(`slug: test-scene
display_name: Renamed
description: updated
`)}
	require.NoError(t, seeder.Run(ctx))

	got, err := q.GetSceneByOwnerSlug(ctx, store.GetSceneByOwnerSlugParams{
		OwnerType: "user", OwnerID: 0, Slug: "test-scene",
	})
	require.NoError(t, err)
	assert.Equal(t, "Renamed", got.DisplayName)
	assert.Equal(t, "updated", got.Description)
}

func TestSceneSeeder_ProductionEmbedHasYAMLs(t *testing.T) {
	// Sanity check that the //go:embed picks up the synced files. If the
	// Makefile target wasn't run, this fails fast at test time rather than
	// silently producing an empty seed at boot.
	entries, err := builtinScenesFS.ReadDir("builtin_scenes")
	require.NoError(t, err)
	assert.NotEmpty(t, entries, "//go:embed builtin_scenes/*.yaml found nothing — run `make builtin-scenes-sync`")
}

// TestSceneSeeder_ProductionEmbedAllValid guards against a malformed builtin
// scene being silently skipped at boot (Run logs+skips invalid YAML). It seeds
// the real embed and asserts every *.yaml round-trips into a row, so a parse or
// ValidateSceneDefinition failure in any shipped scene fails the build.
func TestSceneSeeder_ProductionEmbedAllValid(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	q := store.New(db)

	entries, err := builtinScenesFS.ReadDir("builtin_scenes")
	require.NoError(t, err)
	var wantSlugs []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".yaml") {
			wantSlugs = append(wantSlugs, strings.TrimSuffix(e.Name(), ".yaml"))
		}
	}
	require.NotEmpty(t, wantSlugs)

	require.NoError(t, NewSceneSeeder(q).Run(ctx))

	// Every embedded YAML must have produced a builtin row (file name == slug by
	// convention). A skipped invalid scene shows up here as a missing row.
	for _, slug := range wantSlugs {
		_, err := q.GetSceneByOwnerSlug(ctx, store.GetSceneByOwnerSlugParams{
			OwnerType: "user", OwnerID: 0, Slug: slug,
		})
		assert.NoErrorf(t, err, "builtin scene %q failed to seed (parse/validate error?)", slug)
	}

	// New office scenes specifically.
	for _, slug := range []string{"office-doc", "office-design"} {
		assert.Contains(t, wantSlugs, slug, "expected %q embedded — run `make builtin-scenes-sync`", slug)
	}
}

// TestSceneSeeder_FileBatchSceneGated guards issue #390's core invariant: the
// file-operation (filesystem) MCP is mounted ONLY by the file-batch scene and
// is invisible everywhere else (no global injection / no other builtin exposes
// it). It also asserts the filesystem server is declared inline (command=npx),
// so it works without the Claude registry pre-installing it (team edition /
// fresh machine).
func TestSceneSeeder_FileBatchSceneGated(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	q := store.New(db)
	require.NoError(t, NewSceneSeeder(q).Run(ctx))

	// file-batch must seed and carry an inline `filesystem` MCP server.
	fb, err := q.GetSceneByOwnerSlug(ctx, store.GetSceneByOwnerSlugParams{
		OwnerType: "user", OwnerID: 0, Slug: "file-batch",
	})
	require.NoError(t, err, "file-batch scene must seed — run `make builtin-scenes-sync`")
	assert.Equal(t, "文件批量处理", fb.DisplayName)

	var fbDef SceneDefinition
	require.NoError(t, json.Unmarshal([]byte(fb.Definition), &fbDef))
	var fsDecl *MCPDecl
	for i := range fbDef.MCP {
		if fbDef.MCP[i].Name == "filesystem" {
			fsDecl = &fbDef.MCP[i]
		}
	}
	require.NotNil(t, fsDecl, "file-batch must declare a `filesystem` MCP server")
	require.NotEmpty(t, fsDecl.Config, "filesystem MCP must be inline-configured (registry-independent)")
	assert.Equal(t, "npx", fsDecl.Config["command"], "filesystem MCP launches via npx")
	// No browser/system RPA bundled here — goal #4 (reuse the playwright scene).
	assert.Empty(t, fbDef.Plugins, "file-batch must not bundle plugins (browser RPA reuses the playwright scene)")

	// Gating: no OTHER builtin scene may expose a filesystem / file-operation MCP.
	entries, err := builtinScenesFS.ReadDir("builtin_scenes")
	require.NoError(t, err)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		slug := strings.TrimSuffix(e.Name(), ".yaml")
		if slug == "file-batch" {
			continue
		}
		row, err := q.GetSceneByOwnerSlug(ctx, store.GetSceneByOwnerSlugParams{
			OwnerType: "user", OwnerID: 0, Slug: slug,
		})
		require.NoError(t, err)
		var def SceneDefinition
		require.NoError(t, json.Unmarshal([]byte(row.Definition), &def))
		for _, m := range def.MCP {
			assert.NotEqualf(t, "filesystem", m.Name,
				"scene %q must not expose the filesystem MCP — it is gated to file-batch only", slug)
		}
	}
}
