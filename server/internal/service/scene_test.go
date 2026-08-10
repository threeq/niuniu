package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateSceneDefinition_AcceptsMinimal(t *testing.T) {
	require.NoError(t, ValidateSceneDefinition(&SceneDefinition{}))
}

func TestValidateSceneDefinition_RejectsNil(t *testing.T) {
	assert.Error(t, ValidateSceneDefinition(nil))
}

func TestValidateSceneDefinition_RejectsDuplicateMCP(t *testing.T) {
	def := &SceneDefinition{MCP: []MCPDecl{{Name: "memory"}, {Name: "memory"}}}
	assert.ErrorContains(t, ValidateSceneDefinition(def), "duplicate mcp")
}

func TestValidateSceneDefinition_RejectsEmptyMCPName(t *testing.T) {
	def := &SceneDefinition{MCP: []MCPDecl{{Name: ""}}}
	assert.ErrorContains(t, ValidateSceneDefinition(def), "missing name")
}

func TestValidateSceneDefinition_RejectsBadPluginSource(t *testing.T) {
	def := &SceneDefinition{Plugins: []PluginDecl{{Source: "evil://example"}}}
	assert.ErrorContains(t, ValidateSceneDefinition(def), "not allowed")
}

func TestValidateSceneDefinition_AcceptsGithubColonPlugin(t *testing.T) {
	def := &SceneDefinition{Plugins: []PluginDecl{{Source: "github:foo/bar"}}}
	assert.NoError(t, ValidateSceneDefinition(def))
}

func TestValidateSceneDefinition_AcceptsHTTPSGithubPlugin(t *testing.T) {
	def := &SceneDefinition{Plugins: []PluginDecl{{Source: "https://github.com/foo/bar"}}}
	assert.NoError(t, ValidateSceneDefinition(def))
}

func TestValidateSceneDefinition_AcceptsFilePlugin(t *testing.T) {
	def := &SceneDefinition{Plugins: []PluginDecl{{Source: "file:///abs/path"}}}
	assert.NoError(t, ValidateSceneDefinition(def))
}

func TestValidateSceneDefinition_RejectsCredentialMissingAlias(t *testing.T) {
	def := &SceneDefinition{RequiredCredentials: []RequiredCredential{{Alias: "", Provider: "slack"}}}
	assert.ErrorContains(t, ValidateSceneDefinition(def), "missing alias")
}

func TestValidateSceneDefinition_RejectsCredentialMissingProvider(t *testing.T) {
	def := &SceneDefinition{RequiredCredentials: []RequiredCredential{{Alias: "x", Provider: ""}}}
	assert.ErrorContains(t, ValidateSceneDefinition(def), "missing alias or provider")
}

func TestValidateSceneDefinition_RejectsEmptyAssetSlug(t *testing.T) {
	def := &SceneDefinition{Assets: SceneAssets{
		EnvPresets: []EnvPresetAsset{{Slug: ""}},
	}}
	assert.ErrorContains(t, ValidateSceneDefinition(def), "env_preset asset missing slug")
}

func TestValidateSceneDefinition_RejectsDuplicateAssetSlug(t *testing.T) {
	def := &SceneDefinition{Assets: SceneAssets{
		QuickActions: []QuickActionAsset{{Slug: "x"}, {Slug: "x"}},
	}}
	assert.ErrorContains(t, ValidateSceneDefinition(def), "duplicate quick_action asset slug")
}

func TestValidateSceneDefinition_RejectsPromptMissingID(t *testing.T) {
	def := &SceneDefinition{Prompts: []PromptFragment{{ID: "", Title: "x"}}}
	assert.ErrorContains(t, ValidateSceneDefinition(def), "missing id")
}

func TestHashDefinition_Deterministic(t *testing.T) {
	a := &SceneDefinition{MCP: []MCPDecl{{Name: "x"}}}
	b := &SceneDefinition{MCP: []MCPDecl{{Name: "x"}}}
	assert.Equal(t, HashDefinition(a), HashDefinition(b))
}

func TestHashDefinition_DiffersOnChange(t *testing.T) {
	a := &SceneDefinition{MCP: []MCPDecl{{Name: "x"}}}
	b := &SceneDefinition{MCP: []MCPDecl{{Name: "y"}}}
	assert.NotEqual(t, HashDefinition(a), HashDefinition(b))
}

func TestSlugifyForAsset(t *testing.T) {
	cases := map[string]string{
		"Hello World":      "hello-world",
		"  spaces  ":       "spaces",
		"Special!@#chars":  "special-chars",
		"":                 "untitled",
		"---":              "untitled",
		"客服支持":             "untitled", // CJK collapses to empty under current rules; OK fallback
		"go-dev":           "go-dev",
		"已 经 有 slug":       "slug",
		"UPPER_CASE_THING": "upper-case-thing",
	}
	for in, want := range cases {
		assert.Equal(t, want, SlugifyForAsset(in), "input=%q", in)
	}
}

func TestParsePluginSource(t *testing.T) {
	cases := []struct {
		src         string
		marketplace string
		name        string
		ok          bool
	}{
		{"superpowers@claude-plugins-official", "claude-plugins-official", "superpowers", true},
		{"document-skills@anthropic-agent-skills", "anthropic-agent-skills", "document-skills", true},
		{"github:foo/bar", "github-foo", "bar", true},
		{"https://github.com/foo/bar", "github-foo", "bar", true},
		{"https://github.com/foo/bar.git", "github-foo", "bar", true},
		{"file:///opt/local/plugin", "local", "plugin", true},
		{"file:///some/nested/path/to/myplugin", "local", "myplugin", true},
		{"", "", "", false},
		{"@marketplace", "", "", false},
		{"plugin@", "", "", false},
		{"unknown://thing", "", "", false},
		{"github:foo", "", "", false},
	}
	for _, c := range cases {
		mp, n, ok := parsePluginSource(c.src)
		assert.Equal(t, c.ok, ok, "src=%q", c.src)
		if c.ok {
			assert.Equal(t, c.marketplace, mp)
			assert.Equal(t, c.name, n)
		}
	}
}

func TestPluginInstallerIsInstalledMarketplaceSource(t *testing.T) {
	root := t.TempDir()
	markerDir := filepath.Join(root, ".claude", "plugins", "cache", "claude-plugins-official", "superpowers", "1.0.0")
	require.NoError(t, os.MkdirAll(markerDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(markerDir, "plugin.json"), []byte(`{}`), 0o644))

	ok, err := NewPluginInstaller("").IsInstalled(context.Background(), root, PluginDecl{
		Source: "superpowers@claude-plugins-official",
	})
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestTruncStderr(t *testing.T) {
	short := []byte("short message")
	assert.Equal(t, "short message", truncStderr(short))

	long := make([]byte, 5000)
	for i := range long {
		long[i] = 'x'
	}
	out := truncStderr(long)
	assert.True(t, len(out) > 4000 && len(out) < 5000)
	assert.Contains(t, out, "truncated")
}
