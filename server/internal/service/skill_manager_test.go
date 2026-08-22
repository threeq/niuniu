package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestSkillManager builds a manager whose global scope roots at a temp dir
// instead of the real user home. No DB is wired - the workspace scope is not
// exercised here (its resolution is covered by the scene projection tests).
func newTestSkillManager(t *testing.T) *SkillManager {
	t.Helper()
	home := t.TempDir()
	return &SkillManager{
		dataDir: t.TempDir(),
		homeDir: func() (string, error) { return home, nil },
	}
}

func TestSkillDirFor_AllAgents(t *testing.T) {
	assert.Equal(t, filepath.Join(".claude", "skills"), skillDirFor("claude"))
	assert.Equal(t, filepath.Join(".codex", "skills"), skillDirFor("codex"))
	assert.Equal(t, filepath.Join(".qwen", "skills"), skillDirFor("qwen"))
	assert.Equal(t, filepath.Join(".omp", "skills"), skillDirFor("omp"))
	assert.Equal(t, filepath.Join(".goose", "skills"), skillDirFor("goose"))
	// Unknown CLI falls back to the Claude layout.
	assert.Equal(t, filepath.Join(".claude", "skills"), skillDirFor("whatever"))
}

func TestIsSkillPluginSource(t *testing.T) {
	assert.True(t, isSkillPluginSource("document-skills@anthropic-agent-skills"))
	assert.False(t, isSkillPluginSource("slack@claude-plugins-official"))
	assert.False(t, isSkillPluginSource("gopls-lsp@claude-plugins-official"))
	assert.False(t, isSkillPluginSource("github:owner/repo"))
	assert.False(t, isSkillPluginSource("no-at-sign"))

	skills, rest := splitSkillPlugins([]PluginDecl{
		{Source: "document-skills@anthropic-agent-skills"},
		{Source: "slack@claude-plugins-official"},
	})
	require.Len(t, skills, 1)
	require.Len(t, rest, 1)
	assert.Equal(t, "document-skills@anthropic-agent-skills", skills[0].Source)
	assert.Equal(t, "slack@claude-plugins-official", rest[0].Source)
}

func TestReadSkillMeta_FoldedDescription(t *testing.T) {
	name, version, desc := readSkillMeta("---\nname: s1\nversion: 2.0.0\ndescription: >-\n  First line\n  second line\nother: x\n---\nbody")
	assert.Equal(t, "s1", name)
	assert.Equal(t, "2.0.0", version)
	assert.Equal(t, "First line second line", desc)

	name, _, desc = readSkillMeta("---\nname: s2\ndescription: A short one.\n---\n")
	assert.Equal(t, "s2", name)
	assert.Equal(t, "A short one.", desc)
}

// TestSkillManager_InstallIsNotEnable covers the core issue-#666 contract: a
// global install lands in the store and is NOT agent-visible anywhere until
// enabled.
func TestSkillManager_InstallIsNotEnable(t *testing.T) {
	m := newTestSkillManager(t)
	ctx := context.Background()

	require.NoError(t, m.Install(ctx, "site-audit"))

	// Store copy present...
	assert.FileExists(t, filepath.Join(m.storeDir(), "site-audit", "SKILL.md"))
	assert.FileExists(t, filepath.Join(m.storeDir(), "site-audit", niuniuManagedSkillMarker))
	// ...but no agent dir has it: default 不可用.
	home, _ := m.homeDir()
	for _, agent := range SkillAgents {
		assert.NoDirExists(t, filepath.Join(home, skillDirFor(agent), "site-audit"),
			"agent %s must not see a store-only skill", agent)
	}

	// List reports global_installed without enabled locations.
	var info *SkillInfo
	for i := range m.List(ctx, 0) {
		if m.List(ctx, 0)[i].Name == "site-audit" {
			info = &m.List(ctx, 0)[i]
		}
	}
	require.NotNil(t, info)
	assert.True(t, info.GlobalInstalled)
	assert.Empty(t, info.Installed)
}

// TestSkillManager_EnableUpdateDisableUninstallLifecycle walks the full
// lifecycle across two agents at the global scope.
func TestSkillManager_EnableUpdateDisableUninstallLifecycle(t *testing.T) {
	m := newTestSkillManager(t)
	ctx := context.Background()
	home, _ := m.homeDir()
	targets := []SkillTarget{{Agent: "claude", Scope: "global"}, {Agent: "goose", Scope: "global"}}

	// 1. Install + enable at two agents.
	require.NoError(t, m.Install(ctx, "site-audit"))
	for _, r := range m.Enable(ctx, SkillTargetRequest{Name: "site-audit", Targets: targets}) {
		require.Truef(t, r.OK, "enable should succeed: %v", r.Error)
	}
	for _, agent := range []string{"claude", "goose"} {
		dir := filepath.Join(home, skillDirFor(agent), "site-audit")
		assert.FileExists(t, filepath.Join(dir, "SKILL.md"))
		assert.FileExists(t, filepath.Join(dir, niuniuManagedSkillMarker))
	}

	// 2. List shows both enabled locations, no update pending.
	find := func() *SkillInfo {
		var info *SkillInfo
		for i := range m.List(ctx, 0) {
			if m.List(ctx, 0)[i].Name == "site-audit" {
				info = &m.List(ctx, 0)[i]
			}
		}
		return info
	}
	info := find()
	require.NotNil(t, info)
	require.Len(t, info.Installed, 2)
	for _, st := range info.Installed {
		assert.True(t, st.Managed)
		assert.False(t, st.Update, "fresh enable must not flag an update")
	}

	// 3. Tamper with one copy -> update flagged; Update refreshes store + copies.
	installed := filepath.Join(home, skillDirFor("claude"), "site-audit", "SKILL.md")
	require.NoError(t, os.WriteFile(installed, []byte("---\nname: site-audit\n---\ntampered"), 0o644))
	info = find()
	var flagged int
	for _, st := range info.Installed {
		if st.Update {
			flagged++
		}
	}
	assert.Equal(t, 1, flagged, "only the tampered copy flags an update")

	require.NoError(t, m.Update(ctx, "site-audit"))
	info = find()
	for _, st := range info.Installed {
		assert.False(t, st.Update, "update must clear the flag")
	}

	// 4. Disable one agent: copy removed, store kept.
	for _, r := range m.Disable(ctx, SkillTargetRequest{Name: "site-audit", Targets: []SkillTarget{{Agent: "claude", Scope: "global"}}}) {
		require.Truef(t, r.OK, "disable should succeed: %v", r.Error)
	}
	assert.NoDirExists(t, filepath.Join(home, skillDirFor("claude"), "site-audit"))
	assert.DirExists(t, filepath.Join(home, skillDirFor("goose"), "site-audit"))
	assert.DirExists(t, filepath.Join(m.storeDir(), "site-audit"), "disable must keep the store copy")

	// 5. Uninstall removes store + remaining enabled copies.
	require.NoError(t, m.Uninstall(ctx, "site-audit"))
	assert.NoDirExists(t, filepath.Join(m.storeDir(), "site-audit"))
	assert.NoDirExists(t, filepath.Join(home, skillDirFor("goose"), "site-audit"))
}

// TestSkillManager_UserSkillProtection asserts user-authored skill dirs are
// never disabled or uninstalled, but ARE surfaced in the list.
func TestSkillManager_UserSkillProtection(t *testing.T) {
	m := newTestSkillManager(t)
	ctx := context.Background()
	home, _ := m.homeDir()
	userDir := filepath.Join(home, skillDirFor("claude"), "my-own-skill")
	require.NoError(t, os.MkdirAll(userDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(userDir, "SKILL.md"),
		[]byte("---\nname: my-own-skill\ndescription: mine\n---\nhello"), 0o644))

	var info *SkillInfo
	for i := range m.List(ctx, 0) {
		if m.List(ctx, 0)[i].Name == "my-own-skill" {
			info = &m.List(ctx, 0)[i]
		}
	}
	require.NotNil(t, info)
	assert.Equal(t, "user", info.Source)
	require.Len(t, info.Installed, 1)
	assert.False(t, info.Installed[0].Managed)

	target := []SkillTarget{{Agent: "claude", Scope: "global"}}
	res := m.Disable(ctx, SkillTargetRequest{Name: "my-own-skill", Targets: target})
	require.Len(t, res, 1)
	assert.False(t, res[0].OK)
	assert.Contains(t, res[0].Error, "not niuniu-managed")

	err := m.Uninstall(ctx, "my-own-skill")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "user-authored")
	assert.FileExists(t, filepath.Join(userDir, "SKILL.md"), "user skill must survive")
}

// TestSkillManager_UnknownSkill covers unknown-name errors on every action.
func TestSkillManager_UnknownSkill(t *testing.T) {
	m := newTestSkillManager(t)
	ctx := context.Background()
	assert.Error(t, m.Install(ctx, "nope"))
	assert.Error(t, m.Update(ctx, "nope"))
	assert.Error(t, m.Uninstall(ctx, "nope"))
	res := m.Enable(ctx, SkillTargetRequest{Name: "nope", Targets: []SkillTarget{{Agent: "claude", Scope: "global"}}})
	require.Len(t, res, 1)
	assert.False(t, res[0].OK)
}

// TestSkillManager_EnableValidation covers target validation errors.
func TestSkillManager_EnableValidation(t *testing.T) {
	m := newTestSkillManager(t)
	ctx := context.Background()

	// Unknown agent.
	res := m.Enable(ctx, SkillTargetRequest{Name: "site-audit", Targets: []SkillTarget{{Agent: "cursor", Scope: "global"}}})
	require.Len(t, res, 1)
	assert.Contains(t, res[0].Error, "unknown agent")

	// Workspace scope without workspace_id.
	res = m.Enable(ctx, SkillTargetRequest{Name: "site-audit", Targets: []SkillTarget{{Agent: "claude", Scope: "workspace"}}})
	require.Len(t, res, 1)
	assert.Contains(t, res[0].Error, "workspace_id")

	// Marketplace skills are claude-only.
	res = m.Enable(ctx, SkillTargetRequest{Name: "document-skills", Targets: []SkillTarget{{Agent: "goose", Scope: "global"}}})
	require.Len(t, res, 1)
	assert.Contains(t, res[0].Error, "claude-only")
}

// TestSetPluginEnabledInSettings covers the enabledPlugins writer used for
// marketplace skill enable/disable (default-disabled on install).
func TestSetPluginEnabledInSettings(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".claude", "settings.json")

	// Pre-existing unrelated keys must survive.
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(`{"hooks":{"x":1},"enabledPlugins":{"old@mp":true}}`), 0o644))

	require.NoError(t, setPluginEnabledInSettings(p, "document-skills@anthropic-agent-skills", false))
	b, _ := os.ReadFile(p)
	var doc struct {
		Hooks          map[string]any  `json:"hooks"`
		EnabledPlugins map[string]bool `json:"enabledPlugins"`
	}
	require.NoError(t, json.Unmarshal(b, &doc))
	assert.Contains(t, doc.Hooks, "x", "unrelated keys preserved")
	assert.True(t, doc.EnabledPlugins["old@mp"])
	assert.False(t, doc.EnabledPlugins["document-skills@anthropic-agent-skills"])

	require.NoError(t, setPluginEnabledInSettings(p, "document-skills@anthropic-agent-skills", true))
	b, _ = os.ReadFile(p)
	require.NoError(t, json.Unmarshal(b, &doc))
	assert.True(t, doc.EnabledPlugins["document-skills@anthropic-agent-skills"])

	// Non-marketplace ids are refused.
	assert.Error(t, setPluginEnabledInSettings(p, "github:owner/repo", true))
}

// TestSkillManager_CatalogContent asserts the catalog carries the vendored
// skills plus the curated marketplace entry with parsed metadata.
func TestSkillManager_CatalogContent(t *testing.T) {
	m := newTestSkillManager(t)
	catalog := m.ListCatalog(context.Background())
	names := map[string]SkillInfo{}
	for _, c := range catalog {
		names[c.Name] = c
	}
	assert.Contains(t, names, "drawio-skill")
	assert.Contains(t, names, "site-audit")
	assert.Contains(t, names, "info-radar")
	mp, ok := names["document-skills"]
	require.True(t, ok, "marketplace skill must be in the catalog")
	assert.Equal(t, "marketplace", mp.Source)
	assert.Equal(t, "document-skills@anthropic-agent-skills", mp.PluginSource)
	assert.NotEmpty(t, names["drawio-skill"].Description)
	assert.NotEmpty(t, names["drawio-skill"].Version)
}
