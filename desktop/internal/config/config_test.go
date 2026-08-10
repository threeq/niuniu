package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/niuniu-dev/niuniu-desktop/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg, err := config.LoadFrom(path)
	require.NoError(t, err)
	assert.Empty(t, cfg.Connections)
	cfg.Connections = append(cfg.Connections, config.Connection{
		ID: "abc", Name: "Test", Host: "localhost", Port: 3000,
	})
	err = config.SaveTo(cfg, path)
	require.NoError(t, err)
	cfg2, err := config.LoadFrom(path)
	require.NoError(t, err)
	require.Len(t, cfg2.Connections, 1)
	assert.Equal(t, "Test", cfg2.Connections[0].Name)
}

func TestDefaultConfigDir(t *testing.T) {
	dir := config.DefaultDir()
	assert.Contains(t, dir, ".niuniu")
	assert.Contains(t, dir, "desktop")
}

func TestSetDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg, _ := config.LoadFrom(path)
	cfg.Connections = []config.Connection{
		{ID: "a", Name: "A", Host: "1.2.3.4", Port: 3000},
		{ID: "b", Name: "B", Host: "5.6.7.8", Port: 3000, IsDefault: true},
	}
	cfg.SetDefault("a")
	assert.True(t, cfg.Connections[0].IsDefault)
	assert.False(t, cfg.Connections[1].IsDefault)
	_ = os.Remove(path)
}

func TestAIConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg, err := config.LoadFrom(path)
	require.NoError(t, err)

	svcID := cfg.AI.AddAIService("My Bot", "https://bot.example.com")
	require.NotEmpty(t, svcID)
	cfg.AI.HideBuiltin("chatgpt")
	cfg.AI.DefaultServiceID = "claude"
	cfg.AI.LastServiceID = svcID
	pID := cfg.AI.AddPrompt("Summarize", "Summarize the following:", []string{"写作", " 写作 ", "翻译", ""})
	require.NotEmpty(t, pID)

	require.NoError(t, config.SaveTo(cfg, path))
	got, err := config.LoadFrom(path)
	require.NoError(t, err)
	require.Len(t, got.AI.CustomServices, 1)
	assert.Equal(t, "My Bot", got.AI.CustomServices[0].Name)
	assert.True(t, got.AI.IsBuiltinHidden("chatgpt"))
	assert.False(t, got.AI.IsBuiltinHidden("claude"))
	assert.Equal(t, "claude", got.AI.DefaultServiceID)
	assert.Equal(t, svcID, got.AI.LastServiceID)
	require.Len(t, got.AI.Prompts, 1)
	assert.Equal(t, "Summarize the following:", got.AI.Prompts[0].Content)
	// Tags are normalized (trimmed, de-duplicated case-insensitively, empties dropped).
	assert.Equal(t, []string{"写作", "翻译"}, got.AI.Prompts[0].Tags)
}

func TestAIConfigMutators(t *testing.T) {
	var ai config.AIConfig

	// HideBuiltin is idempotent.
	ai.HideBuiltin("gemini")
	ai.HideBuiltin("gemini")
	assert.Len(t, ai.HiddenBuiltins, 1)

	// Remove custom service.
	id := ai.AddAIService("X", "https://x.example.com")
	assert.True(t, ai.RemoveAIService(id))
	assert.False(t, ai.RemoveAIService(id))
	assert.Empty(t, ai.CustomServices)

	// Remove prompt.
	pid := ai.AddPrompt("t", "c", nil)
	assert.True(t, ai.RemovePrompt(pid))
	assert.False(t, ai.RemovePrompt(pid))
	assert.Empty(t, ai.Prompts)
}
