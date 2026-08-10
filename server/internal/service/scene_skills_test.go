package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProjection_MergeFrom_UnionSkills covers skill union-by-name with later
// layer overriding the Optional flag.
func TestProjection_MergeFrom_UnionSkills(t *testing.T) {
	p := NewProjection()
	p.MergeFrom(&SceneDefinition{Skills: []SkillDecl{{Name: "a"}, {Name: "b", Optional: true}}}, LayerOrigin(1))
	p.MergeFrom(&SceneDefinition{Skills: []SkillDecl{{Name: "b"}, {Name: "c"}}}, LayerOrigin(2))

	names := make([]string, len(p.Skills))
	for i, s := range p.Skills {
		names[i] = s.Name
	}
	assert.Equal(t, []string{"a", "b", "c"}, names)
	// "b" appeared first as Optional:true, later layer set Optional:false → later wins.
	assert.False(t, p.Skills[1].Optional, "later layer should override Optional for the same skill name")
	assert.Equal(t, []LayerOrigin{1, 2}, p.Provenance["skill:b"])
}

// TestBuiltinSkills_InfoRadarPresentAndValid asserts the info-radar skill ships
// embedded with its pipeline steps intact (dedup/search/blackboard key), so a
// scene referencing it can materialize it into a workspace.
func TestBuiltinSkills_InfoRadarPresentAndValid(t *testing.T) {
	b, err := builtinSkillsFS.ReadFile("builtin_skills/info-radar/SKILL.md")
	require.NoError(t, err)
	content := string(b)
	assert.Contains(t, content, "name: info-radar")
	assert.Contains(t, content, "blackboard_read")
	assert.Contains(t, content, "WebSearch")
	assert.Contains(t, content, "radar:pushed")
}

func TestIsSafeSkillName(t *testing.T) {
	for _, ok := range []string{"fireworks-tech-graph", "drawio-skill", "a_b.c", "X1"} {
		assert.Truef(t, isSafeSkillName(ok), "%q should be safe", ok)
	}
	for _, bad := range []string{"", ".", "..", "a/b", "../etc", "a b", "a\\b"} {
		assert.Falsef(t, isSafeSkillName(bad), "%q should be rejected", bad)
	}
}

// TestBuiltinSkillsEmbed asserts the //go:embed picked up the synced skill
// payloads (fails fast if `make builtin-skills-sync` was not run).
func TestBuiltinSkillsEmbed(t *testing.T) {
	for _, name := range []string{"fireworks-tech-graph", "drawio-skill", "excalidraw-skill", "geo-citation-audit", "site-audit"} {
		_, err := builtinSkillsFS.Open(filepath.ToSlash(filepath.Join(builtinSkillsRoot, name, "SKILL.md")))
		require.NoErrorf(t, err, "embedded %s/SKILL.md missing — run `make builtin-skills-sync`", name)
	}
}

// TestMaterializeWorkspaceSkills_CopyAndDetach exercises the full lifecycle:
// materialize copies the payload + marker; a later apply with no skills (detach)
// removes the managed dir; a user-authored skill (no marker) is preserved; codex
// workspaces only clean, never write.
func TestMaterializeWorkspaceSkills_CopyAndDetach(t *testing.T) {
	p := &SceneProjector{}
	ws := store.Workspace{ID: 1, CliType: "claude"}

	wsDir := t.TempDir()
	skillsDir := filepath.Join(wsDir, ".claude", "skills")

	// 1. Materialize two skills.
	p.materializeWorkspaceSkills(ws, wsDir, &Projection{Skills: []SkillDecl{
		{Name: "fireworks-tech-graph"}, {Name: "drawio-skill"},
	}})
	assert.FileExists(t, filepath.Join(skillsDir, "fireworks-tech-graph", "SKILL.md"))
	assert.FileExists(t, filepath.Join(skillsDir, "fireworks-tech-graph", niuniuManagedSkillMarker))
	assert.FileExists(t, filepath.Join(skillsDir, "drawio-skill", "SKILL.md"))

	// 2. A user-authored skill (no marker) sitting alongside must survive.
	userSkill := filepath.Join(skillsDir, "my-own-skill")
	require.NoError(t, os.MkdirAll(userSkill, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(userSkill, "SKILL.md"), []byte("---\nname: mine\n---\n"), 0o644))

	// 3. Re-apply with a smaller skill set: drawio dropped, fireworks kept.
	p.materializeWorkspaceSkills(ws, wsDir, &Projection{Skills: []SkillDecl{{Name: "fireworks-tech-graph"}}})
	assert.FileExists(t, filepath.Join(skillsDir, "fireworks-tech-graph", "SKILL.md"))
	assert.NoDirExists(t, filepath.Join(skillsDir, "drawio-skill"), "detached skill should be removed")
	assert.FileExists(t, filepath.Join(userSkill, "SKILL.md"), "user-authored skill must be preserved")

	// 4. Full detach (no skills) removes the managed dir but keeps the user one.
	p.materializeWorkspaceSkills(ws, wsDir, &Projection{})
	assert.NoDirExists(t, filepath.Join(skillsDir, "fireworks-tech-graph"))
	assert.FileExists(t, filepath.Join(userSkill, "SKILL.md"))
}

func TestMaterializeWorkspaceSkills_CodexSkips(t *testing.T) {
	p := &SceneProjector{}
	wsDir := t.TempDir()
	p.materializeWorkspaceSkills(store.Workspace{ID: 2, CliType: "codex"}, wsDir,
		&Projection{Skills: []SkillDecl{{Name: "fireworks-tech-graph"}}})
	assert.NoDirExists(t, filepath.Join(wsDir, ".claude", "skills", "fireworks-tech-graph"),
		"codex workspaces must not materialize Claude skills")
}

// TestGeoAuditSceneSeedsWithSkill asserts the geo-audit builtin scene seeds and
// projects the geo-citation-audit vendored skill.
func TestGeoAuditSceneSeedsWithSkill(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	q := store.New(db)
	require.NoError(t, NewSceneSeeder(q).Run(ctx))

	scene, err := q.GetSceneByOwnerSlug(ctx, store.GetSceneByOwnerSlugParams{
		OwnerType: "user", OwnerID: 0, Slug: "geo-audit",
	})
	require.NoError(t, err, "geo-audit must seed")

	def, err := DecodeDefinition(scene.Definition)
	require.NoError(t, err)
	names := make([]string, len(def.Skills))
	for i, s := range def.Skills {
		names[i] = s.Name
	}
	assert.ElementsMatch(t, []string{"geo-citation-audit"}, names)
}

// TestGeoSeoSceneSeedsWithSiteAudit asserts the geo-seo builtin scene seeds and
// projects the site-audit vendored skill.
func TestGeoSeoSceneSeedsWithSiteAudit(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	q := store.New(db)
	require.NoError(t, NewSceneSeeder(q).Run(ctx))

	scene, err := q.GetSceneByOwnerSlug(ctx, store.GetSceneByOwnerSlugParams{
		OwnerType: "user", OwnerID: 0, Slug: "geo-seo",
	})
	require.NoError(t, err, "geo-seo must seed")

	def, err := DecodeDefinition(scene.Definition)
	require.NoError(t, err)
	names := make([]string, len(def.Skills))
	for i, s := range def.Skills {
		names[i] = s.Name
	}
	assert.ElementsMatch(t, []string{"site-audit"}, names)
}

// TestVizArchitectureSceneSeedsWithSkills asserts the new builtin scene seeds and
// carries the three vendored drawing skills in its definition.
func TestVizArchitectureSceneSeedsWithSkills(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	q := store.New(db)
	require.NoError(t, NewSceneSeeder(q).Run(ctx))

	scene, err := q.GetSceneByOwnerSlug(ctx, store.GetSceneByOwnerSlugParams{
		OwnerType: "user", OwnerID: 0, Slug: "viz-architecture",
	})
	require.NoError(t, err, "viz-architecture must seed")

	def, err := DecodeDefinition(scene.Definition)
	require.NoError(t, err)
	names := make([]string, len(def.Skills))
	for i, s := range def.Skills {
		names[i] = s.Name
	}
	assert.ElementsMatch(t, []string{"fireworks-tech-graph", "drawio-skill", "excalidraw-skill"}, names)
}
