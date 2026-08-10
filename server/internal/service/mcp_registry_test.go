package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeMCPRegistry_List_GlobalAndPlugin(t *testing.T) {
	root := t.TempDir()

	// 1. Write a fake .claude.json with two mcpServers
	homeClaudeJSON := map[string]any{
		"mcpServers": map[string]any{
			"context7": map[string]any{
				"command": "npx",
				"args":    []string{"-y", "@upstash/context7-mcp"},
			},
			"fetch": map[string]any{
				"command": "uvx",
				"args":    []string{"mcp-server-fetch"},
			},
		},
	}
	mustWriteJSON(t, filepath.Join(root, ".claude.json"), homeClaudeJSON)

	// 2. Write .claude/settings.json with one enabled plugin
	settingsPath := filepath.Join(root, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(settingsPath), 0o755)
	mustWriteJSON(t, settingsPath, map[string]any{
		"enabledPlugins": map[string]bool{
			"playwright@claude-plugins-official": true,
		},
	})

	// 3. Write the plugin's .mcp.json under the conventional path
	pluginMCPPath := filepath.Join(
		root, ".claude", "plugins", "cache",
		"claude-plugins-official", "playwright", "1.0.0", ".mcp.json",
	)
	os.MkdirAll(filepath.Dir(pluginMCPPath), 0o755)
	mustWriteJSON(t, pluginMCPPath, map[string]any{
		"mcpServers": map[string]any{
			"playwright": map[string]any{
				"command": "npx",
				"args":    []string{"@playwright/mcp@latest"},
			},
		},
	})

	r := NewClaudeMCPRegistry()
	got, err := r.List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	byName := map[string]KnownMCP{}
	for _, m := range got {
		byName[m.Name] = m
	}

	if len(byName) != 3 {
		t.Fatalf("expected 3 mcp entries (context7, fetch, playwright), got %d: %+v", len(byName), byName)
	}
	if byName["context7"].Source != MCPSourceGlobal {
		t.Errorf("context7 source = %q, want global", byName["context7"].Source)
	}
	if byName["playwright"].Source != MCPSourcePlugin {
		t.Errorf("playwright source = %q, want plugin", byName["playwright"].Source)
	}
	if byName["playwright"].PluginName != "playwright@claude-plugins-official" {
		t.Errorf("playwright plugin_name = %q", byName["playwright"].PluginName)
	}
}

func TestClaudeMCPRegistry_List_DefaultAccountUsesHome(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp) // Windows uses USERPROFILE for os.UserHomeDir
	r := NewClaudeMCPRegistry()
	got, err := r.List("") // empty configDir → default account
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty list (no files in tmp HOME), got %d", len(got))
	}
}

func TestClaudeMCPRegistry_List_MissingFilesReturnEmpty(t *testing.T) {
	r := NewClaudeMCPRegistry()
	got, err := r.List(t.TempDir())
	if err != nil {
		t.Fatalf("expected nil err on missing files, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty list, got %d", len(got))
	}
}

func mustWriteJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}
