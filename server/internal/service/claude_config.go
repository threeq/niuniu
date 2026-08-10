package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"
)

// ErrMCPServerNotFound is returned by SetMCP when the named server exists in
// neither the active mcpServers nor the disabled sidecar.
var ErrMCPServerNotFound = errors.New("mcp server not found")

type ClaudePluginInfo struct {
	ID        string `json:"id"`
	Enabled   bool   `json:"enabled"`
	Installed bool   `json:"installed"`
	Featured  bool   `json:"featured"`
}
type ClaudeMCPInfo struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Source  string `json:"source"`
}
type ClaudeConfigView struct {
	Plugins    []ClaudePluginInfo `json:"plugins"`
	MCPServers []ClaudeMCPInfo    `json:"mcp_servers"`
}

type ClaudeConfigService struct {
	mu          sync.Mutex
	installer   *PluginInstaller
	marketplace *MarketplaceManager
	registry    *ClaudeMCPRegistry
}

func NewClaudeConfigService(installer *PluginInstaller, marketplace *MarketplaceManager, registry *ClaudeMCPRegistry) *ClaudeConfigService {
	return &ClaudeConfigService{
		installer:   installer,
		marketplace: marketplace,
		registry:    registry,
	}
}

func (s *ClaudeConfigService) resolveRoot(configDir string) (string, error) {
	if configDir != "" {
		return configDir, nil
	}
	return os.UserHomeDir()
}
func (s *ClaudeConfigService) settingsPath(root string) string {
	return filepath.Join(root, ".claude", "settings.json")
}
func (s *ClaudeConfigService) globalMCPPath(root string) string {
	return filepath.Join(root, ".claude.json")
}
func (s *ClaudeConfigService) sidecarPath(root string) string {
	return filepath.Join(root, ".claude", "niuniu-disabled-mcp.json")
}

// readJSONObjectKeys reads {"mcpServers":{...}} (wrapped=true, for .claude.json)
// or {name:{...}} (wrapped=false, sidecar). Missing file => empty map.
func readJSONObjectKeys(path string, wrapped bool) (map[string]json.RawMessage, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]json.RawMessage{}, nil
		}
		return nil, err
	}
	if wrapped {
		var doc struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
		}
		if err := json.Unmarshal(b, &doc); err != nil {
			return nil, err
		}
		if doc.MCPServers == nil {
			return map[string]json.RawMessage{}, nil
		}
		return doc.MCPServers, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m == nil {
		return map[string]json.RawMessage{}, nil
	}
	return m, nil
}

func (s *ClaudeConfigService) List(configDir string) (*ClaudeConfigView, error) {
	root, err := s.resolveRoot(configDir)
	if err != nil {
		return nil, err
	}
	view := &ClaudeConfigView{Plugins: []ClaudePluginInfo{}, MCPServers: []ClaudeMCPInfo{}}

	// Try to get the full plugin catalog (installed + available) from the CLI.
	// On failure we fall back to the file-based enabledPlugins construction below.
	if plugins, err := s.listAvailablePlugins(configDir); err == nil && len(plugins) > 0 {
		view.Plugins = plugins
	} else {
		// Fallback: read enabledPlugins from settings.json.
		if b, err := os.ReadFile(s.settingsPath(root)); err == nil {
			var doc struct {
				EnabledPlugins map[string]bool `json:"enabledPlugins"`
			}
			if err := json.Unmarshal(b, &doc); err != nil {
				return nil, err
			}
			for id, en := range doc.EnabledPlugins {
				view.Plugins = append(view.Plugins, ClaudePluginInfo{ID: id, Enabled: en})
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}

		// Mark installed state for plugins via installer.BatchIsInstalled.
		if s.installer != nil && len(view.Plugins) > 0 {
			sources := make([]string, len(view.Plugins))
			for i, p := range view.Plugins {
				sources[i] = p.ID
			}
			installedSources := s.installer.BatchIsInstalled(context.Background(), root, sources)
			installedSet := make(map[string]struct{}, len(installedSources))
			for _, src := range installedSources {
				installedSet[src] = struct{}{}
			}
			for i := range view.Plugins {
				if _, ok := installedSet[view.Plugins[i].ID]; ok {
					view.Plugins[i].Installed = true
				}
			}
		}
	}

	active, err := readJSONObjectKeys(s.globalMCPPath(root), true)
	if err != nil {
		return nil, err
	}
	for name := range active {
		view.MCPServers = append(view.MCPServers, ClaudeMCPInfo{Name: name, Enabled: true, Source: "global"})
	}
	disabled, err := readJSONObjectKeys(s.sidecarPath(root), false)
	if err != nil {
		return nil, err
	}
	for name := range disabled {
		view.MCPServers = append(view.MCPServers, ClaudeMCPInfo{Name: name, Enabled: false, Source: "global"})
	}

	// Sort by name for a stable, alphabetical UI (Go map iteration is random).
	sort.Slice(view.Plugins, func(i, j int) bool { return view.Plugins[i].ID < view.Plugins[j].ID })
	sort.Slice(view.MCPServers, func(i, j int) bool { return view.MCPServers[i].Name < view.MCPServers[j].Name })
	return view, nil
}

// claudePluginListOutput is the JSON shape returned by
// `claude plugin list --available --json`.
type claudePluginListOutput struct {
	Installed []struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	} `json:"installed"`
	Available []struct {
		PluginID        string `json:"pluginId"`
		Name            string `json:"name"`
		MarketplaceName string `json:"marketplaceName"`
	} `json:"available"`
}

// parseClaudePluginList parses the JSON output of `claude plugin list --available --json`
// and returns a merged list of ClaudePluginInfo. This is a pure function so it can be
// unit-tested without spawning a process.
func parseClaudePluginList(data []byte) ([]ClaudePluginInfo, error) {
	var out claudePluginListOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse claude plugin list: %w", err)
	}

	// Build result starting with installed plugins.
	installedSet := make(map[string]struct{}, len(out.Installed))
	result := make([]ClaudePluginInfo, 0, len(out.Installed)+len(out.Available))
	for _, p := range out.Installed {
		if p.ID == "" {
			continue
		}
		installedSet[p.ID] = struct{}{}
		result = append(result, ClaudePluginInfo{
			ID:        p.ID,
			Enabled:   p.Enabled,
			Installed: true,
		})
	}
	// Add available plugins that are not already in installed.
	for _, p := range out.Available {
		if p.PluginID == "" {
			continue
		}
		if _, ok := installedSet[p.PluginID]; ok {
			continue
		}
		result = append(result, ClaudePluginInfo{
			ID:        p.PluginID,
			Enabled:   false,
			Installed: false,
		})
	}
	return result, nil
}

// listAvailablePlugins shells out to `claude plugin list --available --json`
// (with CLAUDE_CONFIG_DIR set to configDir when non-empty, from a neutral cwd)
// and returns the merged plugin catalog. Returns an error on failure so List
// can fall back to the file-based path — never returns a partial list on error.
func (s *ClaudeConfigService) listAvailablePlugins(configDir string) ([]ClaudePluginInfo, error) {
	// Resolve the claude binary the same way PluginInstaller does.
	claudeBin := "claude"
	if s.installer != nil && s.installer.claudeBin != "" {
		claudeBin = s.installer.claudeBin
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		// On Windows the `claude` shim is a .cmd file; invoke via cmd.exe.
		cmd = exec.CommandContext(ctx, "cmd", "/c", claudeBin, "plugin", "list", "--available", "--json")
	} else {
		cmd = exec.CommandContext(ctx, claudeBin, "plugin", "list", "--available", "--json")
	}
	if configDir != "" {
		cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+configDir)
	}
	// Run from a neutral directory so a project's enabledPlugins don't bleed in.
	cmd.Dir = os.TempDir()

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("claude plugin list: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("claude plugin list: empty output")
	}
	return parseClaudePluginList(out)
}

func atomicWriteJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp-" + randSuffix()
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readRawDoc(path string) (map[string]json.RawMessage, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]json.RawMessage{}, nil
		}
		return nil, err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	if doc == nil {
		doc = map[string]json.RawMessage{}
	}
	return doc, nil
}

// SetPlugin flips enabledPlugins[id], preserving other keys.
func (s *ClaudeConfigService) SetPlugin(configDir, id string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	root, err := s.resolveRoot(configDir)
	if err != nil {
		return err
	}
	path := s.settingsPath(root)
	doc, err := readRawDoc(path)
	if err != nil {
		return err
	}
	var ep map[string]bool
	if raw, ok := doc["enabledPlugins"]; ok {
		if err := json.Unmarshal(raw, &ep); err != nil {
			return err
		}
	}
	if ep == nil {
		ep = map[string]bool{}
	}
	ep[id] = enabled
	epRaw, _ := json.Marshal(ep)
	doc["enabledPlugins"] = epRaw
	return atomicWriteJSON(path, doc)
}

// SetMCP soft-disables/restores a bare MCP server by moving its config between
// .claude.json mcpServers and the sidecar. Lossless.
// Returns ErrMCPServerNotFound when the name is in neither location.
// Returns nil without writing when the entry is already in the desired state.
func (s *ClaudeConfigService) SetMCP(configDir, name string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	root, err := s.resolveRoot(configDir)
	if err != nil {
		return err
	}
	mainPath := s.globalMCPPath(root)
	sidePath := s.sidecarPath(root)
	mainDoc, err := readRawDoc(mainPath)
	if err != nil {
		return err
	}
	var servers map[string]json.RawMessage
	if raw, ok := mainDoc["mcpServers"]; ok {
		_ = json.Unmarshal(raw, &servers)
	}
	if servers == nil {
		servers = map[string]json.RawMessage{}
	}
	side, err := readJSONObjectKeys(sidePath, false)
	if err != nil {
		return err
	}

	if enabled {
		cfg, inSide := side[name]
		if inSide {
			// Move from sidecar → main.
			servers[name] = cfg
			delete(side, name)
		} else if _, inMain := servers[name]; inMain {
			// Already enabled — no-op.
			return nil
		} else {
			return ErrMCPServerNotFound
		}
	} else {
		cfg, inMain := servers[name]
		if inMain {
			// Move from main → sidecar.
			side[name] = cfg
			delete(servers, name)
		} else if _, inSide := side[name]; inSide {
			// Already disabled — no-op.
			return nil
		} else {
			return ErrMCPServerNotFound
		}
	}

	sRaw, _ := json.Marshal(servers)
	mainDoc["mcpServers"] = sRaw

	if enabled {
		// Restoring: main gains the entry → write main first.
		if err := atomicWriteJSON(mainPath, mainDoc); err != nil {
			return err
		}
		return atomicWriteJSON(sidePath, side)
	}
	// Disabling: sidecar gains the entry → write sidecar first.
	if err := atomicWriteJSON(sidePath, side); err != nil {
		return err
	}
	return atomicWriteJSON(mainPath, mainDoc)
}

// InstallPlugin installs a plugin into the account's configDir.
func (s *ClaudeConfigService) InstallPlugin(ctx context.Context, configDir, source, ref string) error {
	if s.installer == nil {
		return fmt.Errorf("plugin installer not configured")
	}
	res := s.installer.Apply(ctx, configDir, []PluginDecl{{Source: source, Ref: ref}})
	if len(res) > 0 && res[0].Status == PluginInstallStatusFailed {
		return fmt.Errorf("install %s failed: %s", source, res[0].Stderr)
	}
	return nil
}

// UninstallPlugin removes a plugin from the account's configDir.
func (s *ClaudeConfigService) UninstallPlugin(ctx context.Context, configDir, source string) error {
	if s.installer == nil {
		return fmt.Errorf("plugin installer not configured")
	}
	res := s.installer.Uninstall(ctx, configDir, PluginDecl{Source: source})
	if res.Status == PluginInstallStatusFailed {
		return fmt.Errorf("uninstall %s failed: %s", source, res.Stderr)
	}
	return nil
}

// AddMarketplace registers an additional plugin marketplace in the account's configDir.
func (s *ClaudeConfigService) AddMarketplace(ctx context.Context, configDir, source string) error {
	if s.marketplace == nil {
		return fmt.Errorf("marketplace manager not configured")
	}
	r := s.marketplace.AddForCLI(ctx, "claude", configDir, source)
	if !r.OK {
		return fmt.Errorf("add marketplace %s failed: %s", source, r.Stderr)
	}
	return nil
}
