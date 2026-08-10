package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProjection_MergeFrom_UnionMCP(t *testing.T) {
	p := NewProjection()
	p.MergeFrom(&SceneDefinition{MCP: []MCPDecl{{Name: "a"}, {Name: "b"}}}, BaseLayerOrigin)
	p.MergeFrom(&SceneDefinition{MCP: []MCPDecl{{Name: "b"}, {Name: "c"}}}, LayerOrigin(1))
	assert.Equal(t, []string{"a", "b", "c"}, p.MCPNames)
	assert.Equal(t, []LayerOrigin{BaseLayerOrigin, LayerOrigin(1)}, p.Provenance["mcp:b"])
	assert.Equal(t, []LayerOrigin{LayerOrigin(1)}, p.Provenance["mcp:c"])
}

func TestProjection_MergeFrom_PluginRefOverride(t *testing.T) {
	p := NewProjection()
	p.MergeFrom(&SceneDefinition{Plugins: []PluginDecl{{Source: "github:foo/bar", Ref: "v1"}}}, LayerOrigin(1))
	p.MergeFrom(&SceneDefinition{Plugins: []PluginDecl{{Source: "github:foo/bar", Ref: "v2", Optional: true}}}, LayerOrigin(2))
	require.Len(t, p.Plugins, 1)
	assert.Equal(t, "v2", p.Plugins[0].Ref)
	assert.True(t, p.Plugins[0].Optional)
}

func TestProjection_MergeFrom_PromptLaterBodyWins(t *testing.T) {
	p := NewProjection()
	p.MergeFrom(&SceneDefinition{Prompts: []PromptFragment{{ID: "x", Title: "Old", Body: "first"}}}, LayerOrigin(1))
	p.MergeFrom(&SceneDefinition{Prompts: []PromptFragment{{ID: "x", Title: "New", Body: "second"}}}, LayerOrigin(2))
	require.Len(t, p.Prompts, 1)
	assert.Equal(t, "second", p.Prompts[0].Body)
	assert.Equal(t, "New", p.Prompts[0].Title)
}

func TestProjection_MergeFrom_CredentialNonOptionalWins(t *testing.T) {
	p := NewProjection()
	p.MergeFrom(&SceneDefinition{RequiredCredentials: []RequiredCredential{{Alias: "x", Provider: "slack", Optional: true}}}, LayerOrigin(1))
	p.MergeFrom(&SceneDefinition{RequiredCredentials: []RequiredCredential{{Alias: "x", Provider: "slack", Optional: false}}}, LayerOrigin(2))
	require.Len(t, p.RequiredCredentials, 1)
	assert.False(t, p.RequiredCredentials[0].Optional, "non-optional must win over optional")
}

func TestProjection_MergeFrom_CredentialOptionalLater(t *testing.T) {
	p := NewProjection()
	p.MergeFrom(&SceneDefinition{RequiredCredentials: []RequiredCredential{{Alias: "x", Provider: "slack", Optional: false}}}, LayerOrigin(1))
	p.MergeFrom(&SceneDefinition{RequiredCredentials: []RequiredCredential{{Alias: "x", Provider: "slack", Optional: true}}}, LayerOrigin(2))
	require.Len(t, p.RequiredCredentials, 1)
	assert.False(t, p.RequiredCredentials[0].Optional, "any non-optional declaration locks alias as required")
}

func TestProjection_MergeFrom_EnvPresetSlugOverride(t *testing.T) {
	p := NewProjection()
	p.MergeFrom(&SceneDefinition{Assets: SceneAssets{
		EnvPresets: []EnvPresetAsset{{Slug: "x", Env: map[string]string{"A": "1"}}},
	}}, LayerOrigin(1))
	p.MergeFrom(&SceneDefinition{Assets: SceneAssets{
		EnvPresets: []EnvPresetAsset{{Slug: "x", Env: map[string]string{"A": "2"}}},
	}}, LayerOrigin(2))
	require.Len(t, p.Assets.EnvPresets, 1)
	assert.Equal(t, "2", p.Assets.EnvPresets[0].Env["A"])
	assert.Equal(t, []LayerOrigin{LayerOrigin(1), LayerOrigin(2)}, p.Provenance["env_preset:x"])
}

func TestProjection_MergeFrom_AgentRefDedup(t *testing.T) {
	p := NewProjection()
	// Two stacked scenes both reference the same agent (by name) — the
	// projection must keep a single reference. Covers "相同资源只留一个".
	p.MergeFrom(&SceneDefinition{Assets: SceneAssets{
		Agents: []AgentAsset{{Name: "researcher"}},
	}}, LayerOrigin(1))
	p.MergeFrom(&SceneDefinition{Assets: SceneAssets{
		Agents: []AgentAsset{{Name: "researcher"}},
	}}, LayerOrigin(2))
	require.Len(t, p.Assets.Agents, 1, "same agent name across layers must dedup to one reference")
	assert.Equal(t, "researcher", p.Assets.Agents[0].Name)
	assert.Equal(t, []LayerOrigin{LayerOrigin(1), LayerOrigin(2)}, p.Provenance["agent:researcher"])
}

func TestProjection_MergeFrom_NilSafe(t *testing.T) {
	p := NewProjection()
	p.MergeFrom(nil, LayerOrigin(1))
	assert.Empty(t, p.MCPNames)
}

func TestProjection_MergeFrom_ProvenanceDedup(t *testing.T) {
	p := NewProjection()
	// Same origin merging two scenes that both declare 'memory' should record origin once
	p.MergeFrom(&SceneDefinition{MCP: []MCPDecl{{Name: "memory"}}}, LayerOrigin(7))
	p.MergeFrom(&SceneDefinition{MCP: []MCPDecl{{Name: "memory"}}}, LayerOrigin(7))
	assert.Equal(t, []LayerOrigin{LayerOrigin(7)}, p.Provenance["mcp:memory"])
}

func TestProjection_Digest_StableAcrossMergeOrderOnSameInput(t *testing.T) {
	a := NewProjection()
	a.MergeFrom(&SceneDefinition{MCP: []MCPDecl{{Name: "memory"}}}, LayerOrigin(1))
	b := NewProjection()
	b.MergeFrom(&SceneDefinition{MCP: []MCPDecl{{Name: "memory"}}}, LayerOrigin(1))
	assert.Equal(t, a.Digest(), b.Digest())
}

func TestProjection_Digest_ChangesOnPluginVersion(t *testing.T) {
	a := NewProjection()
	a.MergeFrom(&SceneDefinition{Plugins: []PluginDecl{{Source: "github:x/y", Ref: "v1"}}}, LayerOrigin(1))
	b := NewProjection()
	b.MergeFrom(&SceneDefinition{Plugins: []PluginDecl{{Source: "github:x/y", Ref: "v2"}}}, LayerOrigin(1))
	assert.NotEqual(t, a.Digest(), b.Digest())
}

func TestProjection_Digest_UnaffectedByPromptChanges(t *testing.T) {
	a := NewProjection()
	a.MergeFrom(&SceneDefinition{Prompts: []PromptFragment{{ID: "x", Title: "T", Body: "old"}}}, LayerOrigin(1))
	b := NewProjection()
	b.MergeFrom(&SceneDefinition{Prompts: []PromptFragment{{ID: "x", Title: "T", Body: "new"}}}, LayerOrigin(1))
	// Process digest covers MCP + plugins only, not prompts
	assert.Equal(t, a.Digest(), b.Digest())
}

func TestProjection_RenderCLAUDEMd_Empty(t *testing.T) {
	p := NewProjection()
	assert.Empty(t, p.RenderCLAUDEMdFragment())
}

func TestProjection_RenderCLAUDEMd_SinglePrompt(t *testing.T) {
	p := NewProjection()
	p.MergeFrom(&SceneDefinition{Prompts: []PromptFragment{{
		ID: "tone", Title: "Customer tone", Body: "Be polite",
	}}}, LayerOrigin(1))
	out := p.RenderCLAUDEMdFragment()
	assert.Contains(t, out, ClaudeMdFragmentBegin)
	assert.Contains(t, out, ClaudeMdFragmentEnd)
	assert.Contains(t, out, "## Customer tone")
	assert.Contains(t, out, "Be polite")
}

func TestProjection_RenderCLAUDEMd_MultiplePromptsSeparated(t *testing.T) {
	p := NewProjection()
	p.MergeFrom(&SceneDefinition{Prompts: []PromptFragment{
		{ID: "a", Title: "A", Body: "body-a"},
		{ID: "b", Title: "B", Body: "body-b"},
	}}, LayerOrigin(1))
	out := p.RenderCLAUDEMdFragment()
	assert.Contains(t, out, "## A")
	assert.Contains(t, out, "## B")
	assert.Contains(t, out, "---")
}

func TestReplaceOrAppendCLAUDEMdBlock_AppendsToEmpty(t *testing.T) {
	block := "<!-- BEGIN niuniu scene fragments -->\nhello\n<!-- END niuniu scene fragments -->\n"
	result := ReplaceOrAppendCLAUDEMdBlock("", block)
	assert.Equal(t, block, result)
}

func TestReplaceOrAppendCLAUDEMdBlock_AppendsToExistingContent(t *testing.T) {
	existing := "# Project\n\nUser content.\n"
	block := "<!-- BEGIN niuniu scene fragments -->\nx\n<!-- END niuniu scene fragments -->\n"
	result := ReplaceOrAppendCLAUDEMdBlock(existing, block)
	assert.True(t, strings.HasPrefix(result, "# Project"))
	assert.Contains(t, result, ClaudeMdFragmentBegin)
}

func TestReplaceOrAppendCLAUDEMdBlock_ReplacesExistingBlock(t *testing.T) {
	existing := "# Project\n\n<!-- BEGIN niuniu scene fragments -->\nold\n<!-- END niuniu scene fragments -->\n\nUser tail.\n"
	block := "<!-- BEGIN niuniu scene fragments -->\nnew\n<!-- END niuniu scene fragments -->\n"
	result := ReplaceOrAppendCLAUDEMdBlock(existing, block)
	assert.Contains(t, result, "new")
	assert.NotContains(t, result, "\nold\n")
	assert.Contains(t, result, "# Project")
	assert.Contains(t, result, "User tail.")
}

func TestReplaceOrAppendCLAUDEMdBlock_EmptyBlockStripsExisting(t *testing.T) {
	existing := "# Project\n\n<!-- BEGIN niuniu scene fragments -->\nx\n<!-- END niuniu scene fragments -->\n\nUser tail.\n"
	result := ReplaceOrAppendCLAUDEMdBlock(existing, "")
	assert.NotContains(t, result, ClaudeMdFragmentBegin)
	assert.Contains(t, result, "# Project")
	assert.Contains(t, result, "User tail.")
}
