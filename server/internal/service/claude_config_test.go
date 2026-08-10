package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func cfgPlugin(v *ClaudeConfigView, id string) (ClaudePluginInfo, bool) {
	for _, p := range v.Plugins {
		if p.ID == id {
			return p, true
		}
	}
	return ClaudePluginInfo{}, false
}
func cfgMCP(v *ClaudeConfigView, name string) (ClaudeMCPInfo, bool) {
	for _, m := range v.MCPServers {
		if m.Name == name {
			return m, true
		}
	}
	return ClaudeMCPInfo{}, false
}

func TestClaudeConfigList(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".claude"), 0o755)
	os.WriteFile(filepath.Join(root, ".claude.json"), []byte(`{"mcpServers":{"pencil":{"command":"x","args":[]}}}`), 0o644)
	os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte(`{"enabledPlugins":{"playwright@mp":true,"foo@mp":false}}`), 0o644)
	os.WriteFile(filepath.Join(root, ".claude", "niuniu-disabled-mcp.json"), []byte(`{"fetch":{"command":"y","args":[]}}`), 0o644)

	svc := NewClaudeConfigService(NewPluginInstaller(""), NewMarketplaceManager(""), NewClaudeMCPRegistry())
	cfg, err := svc.List(root)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := cfgPlugin(cfg, "playwright@mp"); !ok || !p.Enabled {
		t.Fatal("playwright should be enabled")
	}
	if p, ok := cfgPlugin(cfg, "foo@mp"); !ok || p.Enabled {
		t.Fatal("foo should be disabled")
	}
	if m, ok := cfgMCP(cfg, "pencil"); !ok || !m.Enabled {
		t.Fatal("pencil should be enabled")
	}
	if m, ok := cfgMCP(cfg, "fetch"); !ok || m.Enabled {
		t.Fatal("fetch should be disabled(sidecar)")
	}
}

func TestSetPluginPreservesOtherKeys(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".claude"), 0o755)
	os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte(`{"theme":"dark","enabledPlugins":{"a@mp":true}}`), 0o644)
	svc := NewClaudeConfigService(NewPluginInstaller(""), NewMarketplaceManager(""), NewClaudeMCPRegistry())
	if err := svc.SetPlugin(root, "a@mp", false); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	var doc map[string]any
	json.Unmarshal(b, &doc)
	if doc["theme"] != "dark" {
		t.Fatal("theme key clobbered")
	}
	ep := doc["enabledPlugins"].(map[string]any)
	if ep["a@mp"] != false {
		t.Fatal("a@mp not disabled")
	}
}

func TestSetMCPSoftDisableRoundtrip(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".claude"), 0o755)
	os.WriteFile(filepath.Join(root, ".claude.json"), []byte(`{"foo":1,"mcpServers":{"pencil":{"command":"x","args":[]}}}`), 0o644)
	svc := NewClaudeConfigService(NewPluginInstaller(""), NewMarketplaceManager(""), NewClaudeMCPRegistry())
	if err := svc.SetMCP(root, "pencil", false); err != nil {
		t.Fatal(err)
	}
	main, _ := readJSONObjectKeys(filepath.Join(root, ".claude.json"), true)
	if _, ok := main["pencil"]; ok {
		t.Fatal("pencil should be gone from mcpServers")
	}
	side, _ := readJSONObjectKeys(filepath.Join(root, ".claude", "niuniu-disabled-mcp.json"), false)
	if _, ok := side["pencil"]; !ok {
		t.Fatal("pencil should be in sidecar")
	}
	if err := svc.SetMCP(root, "pencil", true); err != nil {
		t.Fatal(err)
	}
	main, _ = readJSONObjectKeys(filepath.Join(root, ".claude.json"), true)
	if _, ok := main["pencil"]; !ok {
		t.Fatal("pencil should be restored")
	}
}

// TestParseClaudePluginList verifies the pure JSON-parsing logic for
// `claude plugin list --available --json` output. Installed plugins get
// Installed=true; available-only plugins get Installed=false; duplicates
// (available plugin whose id appears in installed) are deduplicated.
func TestParseClaudePluginList(t *testing.T) {
	input := []byte(`{
		"installed": [
			{"id": "playwright@claude-plugins-official", "enabled": true},
			{"id": "foo@mp", "enabled": false}
		],
		"available": [
			{"pluginId": "playwright@claude-plugins-official", "name": "Playwright", "marketplaceName": "claude-plugins-official"},
			{"pluginId": "browser@mp", "name": "Browser", "marketplaceName": "mp"},
			{"pluginId": "bar@mp", "name": "Bar", "marketplaceName": "mp"}
		]
	}`)

	plugins, err := parseClaudePluginList(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Build a lookup map by ID.
	byID := make(map[string]ClaudePluginInfo, len(plugins))
	for _, p := range plugins {
		byID[p.ID] = p
	}

	// playwright@claude-plugins-official: installed and enabled.
	if p, ok := byID["playwright@claude-plugins-official"]; !ok {
		t.Error("playwright@claude-plugins-official missing")
	} else {
		if !p.Installed {
			t.Error("playwright@claude-plugins-official: want Installed=true")
		}
		if !p.Enabled {
			t.Error("playwright@claude-plugins-official: want Enabled=true")
		}
	}

	// foo@mp: installed but disabled.
	if p, ok := byID["foo@mp"]; !ok {
		t.Error("foo@mp missing")
	} else {
		if !p.Installed {
			t.Error("foo@mp: want Installed=true")
		}
		if p.Enabled {
			t.Error("foo@mp: want Enabled=false")
		}
	}

	// browser@mp: available-only.
	if p, ok := byID["browser@mp"]; !ok {
		t.Error("browser@mp missing")
	} else {
		if p.Installed {
			t.Error("browser@mp: want Installed=false")
		}
		if p.Enabled {
			t.Error("browser@mp: want Enabled=false")
		}
	}

	// bar@mp: available-only.
	if p, ok := byID["bar@mp"]; !ok {
		t.Error("bar@mp missing")
	} else {
		if p.Installed {
			t.Error("bar@mp: want Installed=false")
		}
	}

	// playwright@claude-plugins-official appears only once (no duplicate from available).
	count := 0
	for _, p := range plugins {
		if p.ID == "playwright@claude-plugins-official" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("playwright@claude-plugins-official: want 1 entry, got %d", count)
	}

	// Total: 2 installed + 2 available-only (browser@mp, bar@mp) = 4.
	if len(plugins) != 4 {
		t.Errorf("want 4 plugins total, got %d", len(plugins))
	}
}

// TestClaudeConfigListInstalledFlag verifies that List marks the Installed
// field correctly: a plugin whose cache directory exists under
// <root>/.claude/plugins/cache/<marketplace>/<name>/<ver>/(.mcp.json|plugin.json)
// is marked Installed=true; a plugin with no cache entry is Installed=false.
func TestClaudeConfigListInstalledFlag(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".claude"), 0o755)

	// Both plugins are present in enabledPlugins.
	os.WriteFile(
		filepath.Join(root, ".claude", "settings.json"),
		[]byte(`{"enabledPlugins":{"playwright@mp":true,"notinstalled@mp":false}}`),
		0o644,
	)

	// Simulate the cache layout for "playwright@mp":
	// <root>/.claude/plugins/cache/mp/playwright/<ver>/.mcp.json
	cacheDir := filepath.Join(root, ".claude", "plugins", "cache", "mp", "playwright", "1.0.0")
	os.MkdirAll(cacheDir, 0o755)
	os.WriteFile(filepath.Join(cacheDir, ".mcp.json"), []byte(`{}`), 0o644)

	// "notinstalled@mp" has NO cache directory.

	svc := NewClaudeConfigService(NewPluginInstaller(""), NewMarketplaceManager(""), NewClaudeMCPRegistry())
	cfg, err := svc.List(root)
	if err != nil {
		t.Fatal(err)
	}

	playwright, ok := cfgPlugin(cfg, "playwright@mp")
	if !ok {
		t.Fatal("playwright@mp not found in plugin list")
	}
	if !playwright.Installed {
		t.Errorf("playwright@mp: want Installed=true, got false")
	}

	notInstalled, ok := cfgPlugin(cfg, "notinstalled@mp")
	if !ok {
		t.Fatal("notinstalled@mp not found in plugin list")
	}
	if notInstalled.Installed {
		t.Errorf("notinstalled@mp: want Installed=false, got true")
	}
}
