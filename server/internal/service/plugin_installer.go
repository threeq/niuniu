package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// pluginInstallTimeout bounds each single `claude plugin install` / `codex
// plugin add` invocation. A hung marketplace, registry, or network must not
// block its caller forever — the scene Apply pipeline auto-installs plugins
// synchronously during workspace create / scene attach, and the SPA's manual
// install click would otherwise spin indefinitely too.
const pluginInstallTimeout = 3 * time.Minute

// PluginInstallStatus is the outcome of attempting to apply a single plugin
// declaration. Values are stable wire enums used by the SPA banner.
type PluginInstallStatus string

const (
	PluginInstallStatusInstalled PluginInstallStatus = "installed"
	PluginInstallStatusSkipped   PluginInstallStatus = "skipped" // already present
	PluginInstallStatusFailed    PluginInstallStatus = "failed"
	// PluginInstallStatusPending — scene projection detected this plugin is
	// declared by a layer, but auto-install is OFF: niuniu never runs
	// `claude plugin install` without an explicit user request. The SPA
	// surfaces these in a "click to install" list. The user opts in per
	// workspace via POST /api/workspaces/:id/scene/plugins/install (or per
	// plugin, by passing a filtered `plugins` body).
	PluginInstallStatusPending PluginInstallStatus = "pending"
	// PluginInstallStatusUnsupported — scene declares plugins but the
	// workspace's CLI cannot install them. Codex CLI 0.x has no
	// `codex plugin install` subcommand; codex plugins are MCP servers
	// declared in `~/.codex/config.toml` [mcp_servers.*] segments, which
	// niuniu already projects from scene.MCPNames. Surfacing this as a
	// distinct status lets the SPA show an explanatory banner without
	// triggering retry CTAs.
	PluginInstallStatusUnsupported PluginInstallStatus = "unsupported"
)

// PluginInstallResult is the per-plugin outcome surfaced to the projection
// cache + UI. Stderr is truncated to 4 KB before persisting.
type PluginInstallResult struct {
	Source string              `json:"source"`
	Ref    string              `json:"ref,omitempty"`
	Status PluginInstallStatus `json:"status"`
	Stderr string              `json:"stderr,omitempty"`
}

// PluginInstaller drives plugin install / uninstall shell-out per projected
// plugin declaration. It is goroutine-safe and serializes installs per
// configDir to avoid CLI lock contention.
//
// Supports both `claude plugin <install|uninstall>` and `codex plugin
// <add|remove>` via the cliType arg on the ForCLI methods. The non-suffixed
// methods (Apply / Uninstall / IsInstalled / BatchIsInstalled / Plan) all
// default to claude for backward compatibility with the scene system, which
// was wired before codex plugin support existed.
type PluginInstaller struct {
	claudeBin string // resolved binary; "" → look up "claude" on PATH

	mu    sync.Mutex
	locks map[string]*sync.Mutex // per-configDir lock
}

// pluginCLISpec captures the binary, env var, sub-command verbs, and
// on-disk cache layout that distinguish `claude plugin` from `codex plugin`.
// Centralizing the differences here keeps the rest of the installer
// CLI-agnostic.
type pluginCLISpec struct {
	binary       string // "claude" / "codex"
	envVar       string // "CLAUDE_CONFIG_DIR" / "CODEX_HOME"
	installCmd   string // "install" / "add"
	uninstallCmd string // "uninstall" / "remove"
}

func cliSpecFor(cliType string) pluginCLISpec {
	if cliType == "codex" {
		return pluginCLISpec{
			binary:       "codex",
			envVar:       "CODEX_HOME",
			installCmd:   "add",
			uninstallCmd: "remove",
		}
	}
	// default = claude (also covers empty string from older callers)
	return pluginCLISpec{
		binary:       "claude",
		envVar:       "CLAUDE_CONFIG_DIR",
		installCmd:   "install",
		uninstallCmd: "uninstall",
	}
}

// NewPluginInstaller constructs an installer. Pass "" for claudeBin to use
// the `claude` binary on PATH (the default — see CLAUDE.md "Two agent
// integration paths").
func NewPluginInstaller(claudeBin string) *PluginInstaller {
	return &PluginInstaller{
		claudeBin: claudeBin,
		locks:     map[string]*sync.Mutex{},
	}
}

// Apply installs the given plugin declarations. Per-plugin failures do NOT
// abort the whole call; instead each plugin produces a PluginInstallResult.
// Callers persist the result slice into
// workspace_scene_projection.install_failures so the banner can surface
// retry CTAs.
//
// Install is idempotent: installOne pre-flights an on-disk check and resolves
// already-present plugins to "skipped" without touching disk / network. Scene
// Apply uses this to auto-install scene-provided skills (enabling the scene
// is the user's explicit action); the /api/workspaces/:id/scene/plugins/install
// handler uses it for manual retries of failed rows.
func (pi *PluginInstaller) Apply(ctx context.Context, configDir string, plugins []PluginDecl) []PluginInstallResult {
	return pi.ApplyForCLI(ctx, "claude", configDir, plugins)
}

// ApplyForCLI is the cliType-aware sibling of Apply. cliType is "claude" or
// "codex"; an empty / unknown value falls back to "claude".
func (pi *PluginInstaller) ApplyForCLI(ctx context.Context, cliType, configDir string, plugins []PluginDecl) []PluginInstallResult {
	if len(plugins) == 0 {
		return nil
	}
	lock := pi.lockFor(configDir)
	lock.Lock()
	defer lock.Unlock()

	results := make([]PluginInstallResult, 0, len(plugins))
	for _, p := range plugins {
		results = append(results, pi.installOne(ctx, cliType, configDir, p))
	}
	return results
}

// Plan is the dry-run sibling of Apply: it inspects which plugins are
// already on disk (via IsInstalled) and marks the rest as Pending — without
// invoking `claude plugin install`. Used by the manual InstallPlugins path to
// rebuild the full per-plugin picture (unchanged rows keep their status) and
// by GetProjection's reconcile to converge a stale cache against disk.
func (pi *PluginInstaller) Plan(ctx context.Context, configDir string, plugins []PluginDecl) []PluginInstallResult {
	return pi.PlanForCLI(ctx, "claude", configDir, plugins)
}

// PlanForCLI is the cliType-aware sibling of Plan.
func (pi *PluginInstaller) PlanForCLI(ctx context.Context, cliType, configDir string, plugins []PluginDecl) []PluginInstallResult {
	if len(plugins) == 0 {
		return nil
	}
	// For codex we resolve installed-state in one pass via `codex plugin
	// list` because the on-disk cache layout is not predictable enough for
	// a per-plugin Stat heuristic. For claude we fall back to the cache
	// Stat path on each plugin (same as the legacy IsInstalled).
	installed := pi.installedSet(ctx, cliType, configDir, plugins)
	results := make([]PluginInstallResult, 0, len(plugins))
	for _, p := range plugins {
		res := PluginInstallResult{Source: p.Source, Ref: p.Ref}
		if _, ok := installed[p.Source]; ok {
			res.Status = PluginInstallStatusSkipped
		} else {
			res.Status = PluginInstallStatusPending
		}
		results = append(results, res)
	}
	return results
}

// Uninstall removes a previously-installed plugin. Mirrors Apply: per-call
// failures surface as PluginInstallResult with Status="failed" + stderr; on
// success Status="installed" is reused (the wire enum has no "uninstalled"
// state because the SPA distinguishes by absence in the next IsInstalled
// check, not by status string).
func (pi *PluginInstaller) Uninstall(ctx context.Context, configDir string, p PluginDecl) PluginInstallResult {
	return pi.UninstallForCLI(ctx, "claude", configDir, p)
}

// UninstallForCLI is the cliType-aware sibling of Uninstall.
func (pi *PluginInstaller) UninstallForCLI(ctx context.Context, cliType, configDir string, p PluginDecl) PluginInstallResult {
	lock := pi.lockFor(configDir)
	lock.Lock()
	defer lock.Unlock()

	spec := cliSpecFor(cliType)
	bin := pi.resolveBinary(spec)
	res := PluginInstallResult{Source: p.Source, Ref: p.Ref}
	cmd := exec.CommandContext(ctx, bin, "plugin", spec.uninstallCmd, p.Source)
	if configDir != "" {
		cmd.Env = append(os.Environ(), spec.envVar+"="+configDir)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		res.Status = PluginInstallStatusFailed
		res.Stderr = truncStderr(out)
		return res
	}
	res.Status = PluginInstallStatusInstalled // reused as "operation OK"
	return res
}

// BatchIsInstalled returns the subset of `sources` that are already installed
// under configDir. Lookup is best-effort (same heuristic as IsInstalled);
// false negatives lead the SPA to offer an extra Install click, which is
// idempotent.
func (pi *PluginInstaller) BatchIsInstalled(ctx context.Context, configDir string, sources []string) []string {
	return pi.BatchIsInstalledForCLI(ctx, "claude", configDir, sources)
}

// BatchIsInstalledForCLI is the cliType-aware sibling of BatchIsInstalled.
// For codex we ask the CLI directly (`codex plugin list`) because the cache
// layout is too unpredictable for a Stat heuristic; for claude we keep the
// stat-based fallback.
func (pi *PluginInstaller) BatchIsInstalledForCLI(ctx context.Context, cliType, configDir string, sources []string) []string {
	installed := pi.installedSet(ctx, cliType, configDir, sourcesToDecls(sources))
	out := make([]string, 0, len(installed))
	for _, s := range sources {
		if _, ok := installed[s]; ok {
			out = append(out, s)
		}
	}
	return out
}

func sourcesToDecls(sources []string) []PluginDecl {
	out := make([]PluginDecl, len(sources))
	for i, s := range sources {
		out[i] = PluginDecl{Source: s}
	}
	return out
}

// installedSet returns the set of sources currently installed under
// configDir, keyed by the canonical `name@marketplace` source. For codex we
// invoke `codex plugin list` once and parse its output; for claude we fall
// back to per-plugin Stat checks against the cache directory.
func (pi *PluginInstaller) installedSet(ctx context.Context, cliType, configDir string, plugins []PluginDecl) map[string]struct{} {
	if cliType == "codex" {
		return pi.codexInstalledSet(ctx, configDir, plugins)
	}
	out := map[string]struct{}{}
	for _, p := range plugins {
		if ok, err := pi.IsInstalled(ctx, configDir, p); err == nil && ok {
			out[p.Source] = struct{}{}
		}
	}
	return out
}

// codexInstalledSet shells out to `codex plugin list` and parses the
// `(installed, enabled)` markers. Output is line-based, each plugin row
// looks like `  <name>@<marketplace> (installed, enabled)`. False negatives
// fall back to the user clicking install, which `codex plugin add` makes
// idempotent.
func (pi *PluginInstaller) codexInstalledSet(ctx context.Context, configDir string, plugins []PluginDecl) map[string]struct{} {
	out := map[string]struct{}{}
	if len(plugins) == 0 {
		return out
	}
	spec := cliSpecFor("codex")
	cmd := exec.CommandContext(ctx, pi.resolveBinary(spec), "plugin", "list")
	if configDir != "" {
		cmd.Env = append(os.Environ(), spec.envVar+"="+configDir)
	}
	raw, err := cmd.CombinedOutput()
	if err != nil {
		// CLI not on PATH / not authed — treat as nothing installed; the
		// install path will surface the real error to the user.
		return out
	}
	// Build a set of requested sources for quick membership test.
	want := map[string]struct{}{}
	for _, p := range plugins {
		want[p.Source] = struct{}{}
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Each row: "<name>@<marketplace> (installed, enabled)" or
		//           "<name>@<marketplace> (not installed)".
		open := strings.Index(line, "(")
		if open < 0 {
			continue
		}
		marker := line[open:]
		if !strings.Contains(marker, "installed") || strings.Contains(marker, "not installed") {
			continue
		}
		source := strings.TrimSpace(line[:open])
		if _, ok := want[source]; ok {
			out[source] = struct{}{}
		}
	}
	return out
}

// resolveBinary returns the binary path to invoke for the given cliSpec.
// Falls back to the spec's default name on PATH when no override is set.
func (pi *PluginInstaller) resolveBinary(spec pluginCLISpec) string {
	if spec.binary == "claude" && pi.claudeBin != "" {
		return pi.claudeBin
	}
	return spec.binary
}

func (pi *PluginInstaller) installOne(ctx context.Context, cliType, configDir string, p PluginDecl) PluginInstallResult {
	res := PluginInstallResult{Source: p.Source, Ref: p.Ref}
	// Pre-flight idempotency check — only meaningful for claude where we
	// can stat the cache locally; for codex we let `codex plugin add`
	// short-circuit on its own ("already installed" exits non-zero with a
	// descriptive message, surfaced as Failed + stderr).
	if cliType != "codex" {
		if installed, err := pi.IsInstalled(ctx, configDir, p); err == nil && installed {
			res.Status = PluginInstallStatusSkipped
			return res
		}
	}
	spec := cliSpecFor(cliType)
	args := []string{"plugin", spec.installCmd, p.Source}
	if p.Ref != "" {
		args = append(args, "--ref", p.Ref)
	}
	cmdCtx, cancel := context.WithTimeout(ctx, pluginInstallTimeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, pi.resolveBinary(spec), args...)
	if configDir != "" {
		cmd.Env = append(os.Environ(), spec.envVar+"="+configDir)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		res.Status = PluginInstallStatusFailed
		res.Stderr = truncStderr(out)
		return res
	}
	res.Status = PluginInstallStatusInstalled
	return res
}

// IsInstalled best-effort checks whether the plugin is already present.
// Implementation note: Claude CLI offers no documented JSON list command
// (architecture review 2026-05-18). Fallback approach: scan
// `<configDir>/.claude/plugins/cache/<marketplace>/<plugin>/` for any
// directory matching the source's plugin name. The source-to-cache-path
// mapping is heuristic (Claude's internal layout is private), so a "false
// negative" leads to an extra install call — `claude plugin install` is
// supposed to be idempotent, so this is safe.
func (pi *PluginInstaller) IsInstalled(ctx context.Context, configDir string, p PluginDecl) (bool, error) {
	root, err := pi.cacheRoot(configDir)
	if err != nil {
		return false, err
	}
	marketplace, name, ok := parsePluginSource(p.Source)
	if !ok {
		return false, nil
	}
	pluginDir := filepath.Join(root, marketplace, name)
	stat, err := os.Stat(pluginDir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !stat.IsDir() {
		return false, nil
	}
	// Confirm at least one version subdirectory with .mcp.json or plugin.json.
	entries, err := os.ReadDir(pluginDir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		marker := filepath.Join(pluginDir, e.Name(), ".mcp.json")
		if _, err := os.Stat(marker); err == nil {
			return true, nil
		}
		marker = filepath.Join(pluginDir, e.Name(), "plugin.json")
		if _, err := os.Stat(marker); err == nil {
			return true, nil
		}
	}
	return false, nil
}

func (pi *PluginInstaller) cacheRoot(configDir string) (string, error) {
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("user home dir: %w", err)
		}
		configDir = home
	}
	return filepath.Join(configDir, ".claude", "plugins", "cache"), nil
}

func (pi *PluginInstaller) lockFor(key string) *sync.Mutex {
	pi.mu.Lock()
	defer pi.mu.Unlock()
	if l, ok := pi.locks[key]; ok {
		return l
	}
	l := &sync.Mutex{}
	pi.locks[key] = l
	return l
}

// parsePluginSource accepts the source forms allowed by ValidateSceneDefinition
// and returns (marketplace, pluginName, ok) suited to the on-disk cache path.
// Layouts handled:
//
//	"name@marketplace"                   → ("marketplace", "name")
//	"github:owner/repo"                  → ("github-owner", "repo")
//	"https://github.com/owner/repo"      → ("github-owner", "repo")
//	"https://github.com/owner/repo.git"  → ("github-owner", "repo")
//	"file:///abs/path/to/plugin"         → ("local", "plugin")
//
// Heuristic — Claude CLI's actual mapping is not documented; this matches the
// shape observed in mcp_registry.go (marketplace/plugin/version layout) and
// is good enough for IsInstalled fallback.
func parsePluginSource(src string) (marketplace, name string, ok bool) {
	switch {
	case strings.Contains(src, "@") && !strings.Contains(src, "/"):
		parts := strings.SplitN(src, "@", 2)
		if parts[0] == "" || parts[1] == "" {
			return "", "", false
		}
		return parts[1], parts[0], true
	case strings.HasPrefix(src, "github:"):
		rest := strings.TrimPrefix(src, "github:")
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", "", false
		}
		return "github-" + parts[0], parts[1], true
	case strings.HasPrefix(src, "https://github.com/"):
		rest := strings.TrimPrefix(src, "https://github.com/")
		rest = strings.TrimSuffix(rest, ".git")
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", "", false
		}
		return "github-" + parts[0], parts[1], true
	case strings.HasPrefix(src, "file:///"):
		path := strings.TrimPrefix(src, "file:///")
		return "local", filepath.Base(filepath.Clean(path)), true
	}
	return "", "", false
}

func truncStderr(b []byte) string {
	const max = 4096
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "\n... (truncated)"
}

// SortInstallResults returns results in deterministic order by source.
func SortInstallResults(in []PluginInstallResult) []PluginInstallResult {
	out := make([]PluginInstallResult, len(in))
	copy(out, in)
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out
}

// asJSON marshals install results for the projection cache. Kept here so the
// caller doesn't need to import encoding/json just to persist.
func InstallResultsToJSON(in []PluginInstallResult) string {
	if len(in) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(in)
	return string(b)
}

// DecodeInstallResults parses a persisted install_failures column back into a
// result slice, tolerating empty / malformed values (treated as none).
func DecodeInstallResults(raw string) []PluginInstallResult {
	if raw == "" {
		return nil
	}
	var out []PluginInstallResult
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}
