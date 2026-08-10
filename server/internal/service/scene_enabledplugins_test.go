package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSetWorkspaceEnabledPlugins(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".claude"), 0o755)
	os.WriteFile(filepath.Join(dir, ".claude", "settings.json"),
		[]byte(`{"hooks":{"x":1},"enabledPlugins":{"a@mp":true}}`), 0o644)
	g := &MCPConfigGenerator{} // zero-value OK; SetWorkspaceEnabledPlugins must not need other fields
	// Pass a canonical marketplace id plus a github: source (which must be skipped).
	if err := g.SetWorkspaceEnabledPlugins(dir, []string{
		"playwright@claude-plugins-official",
		"github:owner/repo", // non-marketplace source — must NOT appear in enabledPlugins
	}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	var doc map[string]any
	json.Unmarshal(b, &doc)
	if _, ok := doc["hooks"]; !ok {
		t.Fatal("hooks clobbered")
	}
	ep := doc["enabledPlugins"].(map[string]any)
	if ep["a@mp"] != true {
		t.Fatal("existing enabledPlugins lost")
	}
	if ep["playwright@claude-plugins-official"] != true {
		t.Fatal("new plugin not enabled")
	}
	if _, ok := ep["github:owner/repo"]; ok {
		t.Fatal("github: source must not be written to enabledPlugins")
	}
}
