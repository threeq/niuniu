package service

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/semver"
)

// MCPSource identifies where a KnownMCP entry was sourced from.
type MCPSource string

const (
	MCPSourceGlobal MCPSource = "global"
	MCPSourcePlugin MCPSource = "plugin"
)

// KnownMCP is one entry from a Claude install's MCP registry — either a
// global mcpServers entry or an enabled plugin's own .mcp.json entry.
type KnownMCP struct {
	Name       string            `json:"name"`
	Command    string            `json:"command"`
	Args       []string          `json:"args"`
	Env        map[string]string `json:"env,omitempty"`
	Source     MCPSource         `json:"source"`
	PluginName string            `json:"plugin_name,omitempty"`
}

// ClaudeMCPRegistry reads a Claude install (default account or isolated one)
// and returns the set of MCP servers that the account can launch.
type ClaudeMCPRegistry struct{}

// NewClaudeMCPRegistry constructs the registry. Stateless.
func NewClaudeMCPRegistry() *ClaudeMCPRegistry { return &ClaudeMCPRegistry{} }

// List returns all MCP servers visible to the given Claude account.
// configDir == "" → default account (use $HOME).
// configDir != "" → isolated account directory (paths.ClaudeAccountDir).
// Read failures on individual files are logged and skipped — never fail the
// whole call because one plugin manifest is malformed.
func (r *ClaudeMCPRegistry) List(configDir string) ([]KnownMCP, error) {
	root, err := r.resolveRoot(configDir)
	if err != nil {
		return nil, err
	}
	if root == "" {
		return nil, nil
	}

	byName := map[string]KnownMCP{}

	// 1. Global mcpServers from <root>/.claude.json
	r.collectGlobal(root, byName)

	// 2. Plugin-bundled MCP from <root>/.claude/plugins/cache/.../<.mcp.json>
	r.collectPlugins(root, byName)

	out := make([]KnownMCP, 0, len(byName))
	for _, m := range byName {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (r *ClaudeMCPRegistry) resolveRoot(configDir string) (string, error) {
	if configDir != "" {
		return configDir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home dir: %w", err)
	}
	return home, nil
}

func (r *ClaudeMCPRegistry) collectGlobal(root string, out map[string]KnownMCP) {
	path := filepath.Join(root, ".claude.json")
	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("mcp_registry: read .claude.json failed", "path", path, "err", err)
		}
		return
	}
	var doc struct {
		MCPServers map[string]rawMCPEntry `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		slog.Warn("mcp_registry: parse .claude.json failed", "path", path, "err", err)
		return
	}
	for name, entry := range doc.MCPServers {
		out[name] = KnownMCP{
			Name:    name,
			Command: entry.Command,
			Args:    entry.Args,
			Env:     entry.Env,
			Source:  MCPSourceGlobal,
		}
	}
}

func (r *ClaudeMCPRegistry) collectPlugins(root string, out map[string]KnownMCP) {
	settingsPath := filepath.Join(root, ".claude", "settings.json")
	b, err := os.ReadFile(settingsPath)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("mcp_registry: read settings.json failed", "path", settingsPath, "err", err)
		}
		return
	}
	var settings struct {
		EnabledPlugins map[string]bool `json:"enabledPlugins"`
	}
	if err := json.Unmarshal(b, &settings); err != nil {
		slog.Warn("mcp_registry: parse settings.json failed", "path", settingsPath, "err", err)
		return
	}
	for pluginID, enabled := range settings.EnabledPlugins {
		if !enabled {
			continue
		}
		pluginName, marketplace, ok := splitPluginID(pluginID)
		if !ok {
			slog.Warn("mcp_registry: malformed plugin id", "id", pluginID)
			continue
		}
		pluginRoot := filepath.Join(root, ".claude", "plugins", "cache", marketplace, pluginName)
		version := pickPluginVersion(pluginRoot)
		if version == "" {
			continue
		}
		mcpPath := filepath.Join(pluginRoot, version, ".mcp.json")
		bb, err := os.ReadFile(mcpPath)
		if err != nil {
			if !os.IsNotExist(err) {
				slog.Warn("mcp_registry: read plugin mcp failed", "path", mcpPath, "err", err)
			}
			continue
		}
		entries := parsePluginMCP(bb)
		for name, entry := range entries {
			out[name] = KnownMCP{
				Name:       name,
				Command:    entry.Command,
				Args:       entry.Args,
				Env:        entry.Env,
				Source:     MCPSourcePlugin,
				PluginName: pluginID,
			}
		}
	}
}

// splitPluginID parses "playwright@claude-plugins-official" into ("playwright", "claude-plugins-official", true).
func splitPluginID(id string) (plugin, marketplace string, ok bool) {
	idx := strings.LastIndex(id, "@")
	if idx <= 0 || idx == len(id)-1 {
		return "", "", false
	}
	return id[:idx], id[idx+1:], true
}

// pickPluginVersion returns the chosen version directory under pluginRoot.
// Strategy: try SemVer max; fall back to lexicographic max on non-SemVer (e.g. "unknown").
func pickPluginVersion(pluginRoot string) string {
	entries, err := os.ReadDir(pluginRoot)
	if err != nil {
		return ""
	}
	var versions []string
	for _, e := range entries {
		if e.IsDir() {
			versions = append(versions, e.Name())
		}
	}
	if len(versions) == 0 {
		return ""
	}
	// Try SemVer first
	var semverDirs []string
	for _, v := range versions {
		if semver.IsValid("v" + v) {
			semverDirs = append(semverDirs, v)
		}
	}
	if len(semverDirs) > 0 {
		sort.Slice(semverDirs, func(i, j int) bool {
			return semver.Compare("v"+semverDirs[i], "v"+semverDirs[j]) > 0
		})
		return semverDirs[0]
	}
	// Fallback: lexicographic
	sort.Strings(versions)
	return versions[len(versions)-1]
}

type rawMCPEntry struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
}

// parsePluginMCP handles both shapes:
//
//	{ "mcpServers": { ... } }   ← like global
//	{ "<name>": { ... } }        ← some plugins inline (e.g. playwright's .mcp.json)
func parsePluginMCP(b []byte) map[string]rawMCPEntry {
	out := map[string]rawMCPEntry{}
	var wrapped struct {
		MCPServers map[string]rawMCPEntry `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &wrapped); err == nil && len(wrapped.MCPServers) > 0 {
		return wrapped.MCPServers
	}
	// Try inline shape
	var inline map[string]rawMCPEntry
	if err := json.Unmarshal(b, &inline); err == nil {
		for k, v := range inline {
			// Filter out non-MCP top-level fields like "mcpServers" or accidental ones
			if v.Command != "" {
				out[k] = v
			}
		}
	}
	return out
}
