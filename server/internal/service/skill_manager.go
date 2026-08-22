// Package service: cross-agent skill management (SkillsGate-style).
//
// A dedicated manager for Agent Skills (SKILL.md payloads) across the CLI
// agents niuniu drives - claude / codex / qwen / omp / goose. Install and
// enable are deliberately SEPARATE (issue #666):
//
//   - INSTALL (global)   <dataDir>/skills/<name>/        niuniu's store - on
//                        disk but NOT agent-visible anywhere. A globally
//                        installed skill is disabled by default so it never
//                        bloats every agent's context.
//   - ENABLE (global)    ~/<agent-dir>/skills/<name>/    agent-visible in every
//                        workspace of that agent.
//   - ENABLE (workspace) <wsDir>/<agent-dir>/skills/     agent-visible in that
//                        workspace only - what a scene does when it declares
//                        the skill (and only then; scene enablement is scoped
//                        to the workspace).
//
// The catalog has two sources:
//
//   - builtin     the vendored skills embedded in the binary (builtin_skills/,
//                 the same payloads scenes project) - full install / enable /
//                 update / uninstall lifecycle via local file copies
//   - marketplace curated skill-type plugins (document-skills@anthropic-
//                 agent-skills) installed through `claude plugin install`.
//                 "Install" is the plugin install; "enable" flips the
//                 enabledPlugins switch in the matching settings.json (home =
//                 global scope, workspace dir = workspace scope), so a plugin
//                 is likewise invisible until enabled.
//
// niuniu-managed copies carry the .niuniu-managed marker (same contract as
// scene-projected skills) so disable/uninstall never touches user-authored
// skills.
package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// skillDirFor returns the skills directory (relative to an agent root - the
// user home for the global scope, the workspace dir for the workspace scope)
// for a CLI type. All five agents niuniu drives follow the same
// ".<cli>/skills" convention for the open Agent Skills format.
func skillDirFor(agent string) string {
	switch agent {
	case "codex":
		return filepath.Join(".codex", "skills")
	case "qwen":
		return filepath.Join(".qwen", "skills")
	case "omp":
		return filepath.Join(".omp", "skills")
	case "goose":
		return filepath.Join(".goose", "skills")
	default: // claude (and any unknown CLI) keep the historic layout.
		return filepath.Join(".claude", "skills")
	}
}

// SkillAgents is the ordered set of agents the skill manager covers.
var SkillAgents = []string{"claude", "codex", "qwen", "omp", "goose"}

// marketplaceSkillPlugins is the curated set of skill-type marketplace plugins
// surfaced in the catalog. They are pure Agent Skills bundles (not
// external-service MCP integrations), so installing them is a "skill" action.
type MarketplaceSkillDecl struct {
	Source      string `json:"source"` // canonical plugin source
	Name        string `json:"name"`
	Description string `json:"description"`
}

var marketplaceSkillPlugins = []MarketplaceSkillDecl{
	{
		Source:      "document-skills@anthropic-agent-skills",
		Name:        "document-skills",
		Description: "Anthropic 官方文档技能包：xlsx / docx / pptx / pdf 生成与编辑",
	},
}

// skillPluginMarketplaces lists plugin marketplaces whose payloads are pure
// Agent Skills bundles (SKILL.md), not external-service MCP integrations.
// Scene-declared plugins from these marketplaces auto-install with the scene
// (issue #666: 场景自带的 skill 自动安装); everything else (slack@…, gopls-lsp@…
// - plugins that wire up external services) keeps the explicit
// install-click flow in the projection banner.
var skillPluginMarketplaces = map[string]bool{
	"anthropic-agent-skills": true,
}

// isSkillPluginSource reports whether a plugin source points at a skill-type
// marketplace (name@<skill-marketplace>).
func isSkillPluginSource(source string) bool {
	at := strings.LastIndexByte(source, '@')
	return at > 0 && at < len(source)-1 && skillPluginMarketplaces[source[at+1:]]
}

// splitSkillPlugins partitions declarations into (skill-type, rest).
func splitSkillPlugins(decls []PluginDecl) (skills, rest []PluginDecl) {
	for _, d := range decls {
		if isSkillPluginSource(d.Source) {
			skills = append(skills, d)
		} else {
			rest = append(rest, d)
		}
	}
	return skills, rest
}

// SkillTarget identifies one enable location: an agent CLI at a scope.
type SkillTarget struct {
	Agent string `json:"agent"` // claude | codex | qwen | omp | goose
	Scope string `json:"scope"` // global | workspace
}

// SkillInstallState reports one location a skill is ENABLED (agent-visible) at.
type SkillInstallState struct {
	Agent   string `json:"agent"`
	Scope   string `json:"scope"`
	Managed bool   `json:"managed"`          // carries the niuniu marker
	Update  bool   `json:"update,omitempty"` // builtin payload differs from the enabled copy
}

// SkillInfo is one skill in the catalog, enriched with install/enable state.
type SkillInfo struct {
	Name         string              `json:"name"`
	Description  string              `json:"description,omitempty"`
	Version      string              `json:"version,omitempty"`
	Source       string              `json:"source"` // builtin | marketplace | user
	PluginSource string              `json:"plugin_source,omitempty"`
	GlobalInstalled bool             `json:"global_installed"` // installed into the store / claude plugin cache (NOT yet enabled)
	Installed    []SkillInstallState `json:"installed"`        // ENABLED locations
}

// SkillTargetRequest is the body for the enable / disable / workspace-install
// endpoints: one skill, one or more targets.
type SkillTargetRequest struct {
	Name        string        `json:"name" binding:"required"`
	WorkspaceID int64         `json:"workspace_id"` // required for workspace-scope targets
	Targets     []SkillTarget `json:"targets" binding:"required,min=1"`
}

// SkillActionResult reports the per-target outcome of an action.
type SkillActionResult struct {
	Target SkillTarget `json:"target"`
	OK     bool        `json:"ok"`
	Error  string      `json:"error,omitempty"`
}

// SkillManager drives the skill lifecycle across agents and scopes.
type SkillManager struct {
	q          *store.Queries
	dataDir    string
	pluginInst *PluginInstaller // marketplace skill plugins; nil-safe

	// homeDir resolves the user home for global-scope paths. Defaults to
	// os.UserHomeDir; overridable in tests so the suite never touches the real
	// ~/.claude tree.
	homeDir func() (string, error)
}

// NewSkillManager wires the manager. pluginInst may be nil (marketplace
// installs then fail with a clear error; everything local still works).
func NewSkillManager(db *sql.DB, dataDir string, pluginInst *PluginInstaller) *SkillManager {
	return &SkillManager{
		// store.NewQueries - driver-aware; see CLAUDE.md "Driver-aware DB access".
		q:          store.NewQueries(db),
		dataDir:    dataDir,
		pluginInst: pluginInst,
		homeDir:    os.UserHomeDir,
	}
}

// storeDir is niuniu's global skill store (<dataDir>/skills) - installed but
// not agent-visible.
func (m *SkillManager) storeDir() string { return filepath.Join(m.dataDir, "skills") }

// readSkillMeta extracts name / version / description from a SKILL.md payload,
// tolerating inline and YAML folded (>-) description values.
func readSkillMeta(content string) (name, version, description string) {
	name = strings.TrimSpace(readFrontmatterScalar(content, "name"))
	version = strings.TrimSpace(readFrontmatterScalar(content, "version"))
	header, _, ok := splitFrontmatter(content)
	if ok {
		var lines []string
		inDesc := false
		for _, line := range strings.Split(header, "\n") {
			if topLevelKey(line) == "description" {
				colon := strings.Index(line, ":")
				val := strings.TrimSpace(line[colon+1:])
				val = strings.TrimPrefix(val, ">-")
				val = strings.TrimPrefix(val, ">")
				val = strings.TrimSpace(val)
				if val != "" {
					lines = append(lines, val)
				}
				inDesc = true
				continue
			}
			if inDesc {
				// Folded blocks continue as indented lines; a top-level key ends it.
				if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
					lines = append(lines, strings.TrimSpace(line))
					continue
				}
				inDesc = false
			}
		}
		description = strings.Join(lines, " ")
	}
	if len(description) > 200 {
		description = description[:200] + "…"
	}
	return name, version, description
}

// ListCatalog returns the installable catalog: embedded builtin skills plus the
// curated marketplace skill plugins.
func (m *SkillManager) ListCatalog(ctx context.Context) []SkillInfo {
	out := make([]SkillInfo, 0, 8)
	entries, err := fs.ReadDir(builtinSkillsFS, builtinSkillsRoot)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			b, err := fs.ReadFile(builtinSkillsFS, path.Join(builtinSkillsRoot, e.Name(), "SKILL.md"))
			if err != nil {
				continue
			}
			name, version, desc := readSkillMeta(string(b))
			if name == "" {
				name = e.Name()
			}
			out = append(out, SkillInfo{Name: name, Version: version, Description: desc, Source: "builtin"})
		}
	}
	for _, mp := range marketplaceSkillPlugins {
		out = append(out, SkillInfo{
			Name: mp.Name, Description: mp.Description,
			Source: "marketplace", PluginSource: mp.Source,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// List returns the catalog enriched with install + enable state, plus
// user-authored skills discovered on disk. When workspaceID > 0 the workspace
// scope of that workspace is included in the scan.
func (m *SkillManager) List(ctx context.Context, workspaceID int64) []SkillInfo {
	catalog := m.ListCatalog(ctx)
	byName := map[string]*SkillInfo{}
	for i := range catalog {
		byName[catalog[i].Name] = &catalog[i]
	}

	// Builtin: installed-into-store state.
	for i := range catalog {
		if catalog[i].Source != "builtin" {
			continue
		}
		catalog[i].GlobalInstalled = fileExists(filepath.Join(m.storeDir(), catalog[i].Name, "SKILL.md"))
	}
	// Marketplace: installed = plugin present in the claude cache; enabled =
	// enabledPlugins flipped on in the settings.json of the matching scope.
	for i := range catalog {
		if catalog[i].Source != "marketplace" || m.pluginInst == nil {
			continue
		}
		installed, _ := m.pluginInst.IsInstalled(ctx, "", PluginDecl{Source: catalog[i].PluginSource})
		catalog[i].GlobalInstalled = installed
		if installed {
			if m.pluginEnabledAt("", catalog[i].PluginSource) { // home settings
				catalog[i].Installed = append(catalog[i].Installed,
					SkillInstallState{Agent: "claude", Scope: "global", Managed: true})
			}
		}
	}

	// Directory scan: enabled copies per agent (global) + workspace scope.
	scanOne := func(absSkillsDir, agent, scope string) {
		entries, err := os.ReadDir(absSkillsDir)
		if err != nil {
			return // missing dir = nothing enabled for this agent/scope
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := filepath.Join(absSkillsDir, e.Name())
			if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
				continue // not a skill directory
			}
			state := SkillInstallState{
				Agent:   agent,
				Scope:   scope,
				Managed: fileExists(filepath.Join(dir, niuniuManagedSkillMarker)),
			}
			if info, ok := byName[e.Name()]; ok && info.Source == "builtin" {
				state.Update = m.builtinDiffers(e.Name(), dir)
				info.Installed = append(info.Installed, state)
				continue
			}
			if _, ok := byName[e.Name()]; ok {
				// marketplace entry already handled above (plugin state), but a
				// directory copy is also a valid enable - record it.
				byName[e.Name()].Installed = append(byName[e.Name()].Installed, state)
				continue
			}
			// Discovered skill not in the catalog - surface as user-authored.
			b, _ := os.ReadFile(filepath.Join(dir, "SKILL.md"))
			name, version, desc := readSkillMeta(string(b))
			if name == "" {
				name = e.Name()
			}
			catalog = append(catalog, SkillInfo{
				Name: name, Version: version, Description: desc,
				Source: "user", Installed: []SkillInstallState{state},
			})
			byName[name] = &catalog[len(catalog)-1]
		}
	}

	for _, agent := range SkillAgents {
		if root, err := m.globalSkillsRoot(agent); err == nil {
			scanOne(root, agent, "global")
		}
	}
	if workspaceID > 0 {
		if ws, err := m.q.GetWorkspace(ctx, workspaceID); err == nil {
			wsDir := OwnerRef{Type: ws.OwnerType, ID: ws.OwnerID}.WorkspacePath(m.dataDir, ws.ID)
			// Scan every agent's workspace skills dir so the per-agent toggle
			// matrix reflects the full state (a workspace may hold skill dirs
			// for CLIs other than its current one after a CliType switch).
			for _, agent := range SkillAgents {
				scanOne(filepath.Join(wsDir, skillDirFor(agent)), agent, "workspace")
			}
			// Marketplace plugin enabled for this workspace?
			for i := range catalog {
				if catalog[i].Source != "marketplace" || catalog[i].PluginSource == "" {
					continue
				}
				if m.pluginEnabledAt(wsDir, catalog[i].PluginSource) {
					catalog[i].Installed = append(catalog[i].Installed,
						SkillInstallState{Agent: "claude", Scope: "workspace", Managed: true})
				}
			}
		}
	}

	sort.Slice(catalog, func(i, j int) bool { return catalog[i].Name < catalog[j].Name })
	return catalog
}

// Install performs a GLOBAL install: builtin skills are copied into the store
// (installed, not yet enabled anywhere); marketplace skill plugins run
// `claude plugin install` and are then left DISABLED globally (enabledPlugins
// flipped off in the home settings.json) - enable flips it back on per scope.
func (m *SkillManager) Install(ctx context.Context, name string) error {
	for _, s := range m.ListCatalog(ctx) {
		if s.Name != name {
			continue
		}
		switch s.Source {
		case "builtin":
			dst := filepath.Join(m.storeDir(), name)
			if err := os.MkdirAll(m.storeDir(), 0o755); err != nil {
				return err
			}
			_ = os.RemoveAll(dst)
			return m.copyBuiltinTo(name, dst)
		case "marketplace":
			if m.pluginInst == nil {
				return fmt.Errorf("plugin installer not wired")
			}
			res := m.pluginInst.ApplyForCLI(ctx, "claude", "", []PluginDecl{{Source: s.PluginSource}})
			if len(res) > 0 && res[0].Status == PluginInstallStatusFailed {
				return fmt.Errorf("%s", res[0].Stderr)
			}
			// Default-disabled: keep the freshly installed plugin off until the
			// user (or a scene, workspace-scoped) enables it.
			return m.setPluginEnabledAt("", s.PluginSource, false)
		}
	}
	return fmt.Errorf("unknown skill %q", name)
}

// Enable makes the skill agent-visible at each requested target. Builtin
// skills are copied from the embedded payload (store is refreshed first);
// marketplace plugins flip enabledPlugins=true in the matching settings.json.
func (m *SkillManager) Enable(ctx context.Context, req SkillTargetRequest) []SkillActionResult {
	results := make([]SkillActionResult, 0, len(req.Targets))
	catalog := m.ListCatalog(ctx)
	var skill *SkillInfo
	for i := range catalog {
		if catalog[i].Name == req.Name {
			skill = &catalog[i]
			break
		}
	}
	if skill == nil {
		for _, t := range req.Targets {
			results = append(results, SkillActionResult{Target: t, Error: "unknown skill"})
		}
		return results
	}

	for _, t := range req.Targets {
		if err := m.validateTarget(t, req.WorkspaceID); err != nil {
			results = append(results, SkillActionResult{Target: t, Error: err.Error()})
			continue
		}
		var err error
		switch skill.Source {
		case "builtin":
			err = m.enableBuiltinAt(req.Name, t, req.WorkspaceID)
		case "marketplace":
			err = m.enableMarketplaceAt(skill.PluginSource, t, req.WorkspaceID)
		default:
			err = fmt.Errorf("skill %q is not enableable", req.Name)
		}
		results = append(results, SkillActionResult{Target: t, OK: err == nil, Error: errString(err)})
	}
	return results
}

// Disable removes the skill's agent visibility at each requested target
// (the global store / plugin install is kept). Only niuniu-managed
// directories are removed; user-authored skills are refused.
func (m *SkillManager) Disable(ctx context.Context, req SkillTargetRequest) []SkillActionResult {
	results := make([]SkillActionResult, 0, len(req.Targets))
	catalog := m.ListCatalog(ctx)
	var skill *SkillInfo
	for i := range catalog {
		if catalog[i].Name == req.Name {
			skill = &catalog[i]
			break
		}
	}
	for _, t := range req.Targets {
		if err := m.validateTarget(t, req.WorkspaceID); err != nil {
			results = append(results, SkillActionResult{Target: t, Error: err.Error()})
			continue
		}
		var err error
		if skill != nil && skill.Source == "marketplace" {
			root := ""
			if t.Scope == "workspace" {
				root, err = m.workspaceDir(ctx, req.WorkspaceID)
				if err == nil {
					err = m.setPluginEnabledAt(root, skill.PluginSource, false)
				}
			} else {
				err = m.setPluginEnabledAt("", skill.PluginSource, false)
			}
		} else {
			dir, derr := m.targetSkillsDir(ctx, t, req.WorkspaceID)
			if derr != nil {
				err = derr
			} else {
				dst := filepath.Join(dir, req.Name)
				if !fileExists(filepath.Join(dst, "SKILL.md")) {
					err = fmt.Errorf("not enabled at target")
				} else if !fileExists(filepath.Join(dst, niuniuManagedSkillMarker)) {
					err = fmt.Errorf("not niuniu-managed (user skill)")
				} else {
					err = os.RemoveAll(dst)
				}
			}
		}
		results = append(results, SkillActionResult{Target: t, OK: err == nil, Error: errString(err)})
	}
	return results
}

// Update refreshes the global store AND every niuniu-managed enabled copy of
// the skill with the latest embedded payload. User-authored directories are
// left untouched.
func (m *SkillManager) Update(ctx context.Context, name string) error {
	src := path.Join(builtinSkillsRoot, name)
	if _, err := fs.Stat(builtinSkillsFS, src); err != nil {
		return fmt.Errorf("builtin skill %q not vendored", name)
	}
	// Refresh the store if the skill is installed there.
	if fileExists(filepath.Join(m.storeDir(), name, "SKILL.md")) {
		dst := filepath.Join(m.storeDir(), name)
		_ = os.RemoveAll(dst)
		if err := m.copyBuiltinTo(name, dst); err != nil {
			return err
		}
	}
	// Refresh every enabled managed copy across all agents' global dirs.
	refreshed := 0
	for _, agent := range SkillAgents {
		root, err := m.globalSkillsRoot(agent)
		if err != nil {
			continue
		}
		dst := filepath.Join(root, name)
		if !fileExists(filepath.Join(dst, niuniuManagedSkillMarker)) {
			continue
		}
		_ = os.RemoveAll(dst)
		if err := m.copyBuiltinTo(name, dst); err == nil {
			refreshed++
		}
	}
	_ = refreshed
	return nil
}

// Uninstall removes the skill globally: the store copy plus every
// niuniu-managed enabled copy across all agents. User-authored skills are
// refused. Marketplace plugins delegate to `claude plugin uninstall`.
func (m *SkillManager) Uninstall(ctx context.Context, name string) error {
	for _, s := range m.ListCatalog(ctx) {
		if s.Name != name {
			continue
		}
		if s.Source == "marketplace" {
			if m.pluginInst == nil {
				return fmt.Errorf("plugin installer not wired")
			}
			res := m.pluginInst.UninstallForCLI(ctx, "claude", "", PluginDecl{Source: s.PluginSource})
			if res.Status == PluginInstallStatusFailed {
				return fmt.Errorf("%s", res.Stderr)
			}
			return nil
		}
		break // builtin: fall through to the directory sweep below
	}
	// Directory-based (builtin or user): remove store + every global copy,
	// refusing if any copy is user-authored.
	found := fileExists(filepath.Join(m.storeDir(), name, "SKILL.md"))
	_ = os.RemoveAll(filepath.Join(m.storeDir(), name))
	for _, agent := range SkillAgents {
		root, err := m.globalSkillsRoot(agent)
		if err != nil {
			continue
		}
		dst := filepath.Join(root, name)
		if !fileExists(filepath.Join(dst, "SKILL.md")) {
			continue
		}
		found = true
		if !fileExists(filepath.Join(dst, niuniuManagedSkillMarker)) {
			return fmt.Errorf("%q has a user-authored copy under %s - remove it manually", name, agent)
		}
		_ = os.RemoveAll(dst)
	}
	if !found {
		return fmt.Errorf("unknown skill %q", name)
	}
	return nil
}

// enableBuiltinAt copies the embedded payload + marker into the target's
// skills dir, replacing any stale dir of the same name.
func (m *SkillManager) enableBuiltinAt(name string, t SkillTarget, workspaceID int64) error {
	dir, err := m.targetSkillsDir(context.Background(), t, workspaceID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(dir, name)
	_ = os.RemoveAll(dst)
	return m.copyBuiltinTo(name, dst)
}

// enableMarketplaceAt flips enabledPlugins=true for the plugin in the
// settings.json matching the target scope (home = global, workspace dir =
// workspace). The plugin system is claude-only. Enabling a not-yet-installed
// plugin installs it first (and leaves other scopes disabled).
func (m *SkillManager) enableMarketplaceAt(pluginSource string, t SkillTarget, workspaceID int64) error {
	if t.Agent != "claude" {
		return fmt.Errorf("marketplace skills are claude-only")
	}
	if m.pluginInst == nil {
		return fmt.Errorf("plugin installer not wired")
	}
	// Auto-install on first enable so the toggle is self-sufficient.
	installed, err := m.pluginInst.IsInstalled(context.Background(), "", PluginDecl{Source: pluginSource})
	if err == nil && !installed {
		res := m.pluginInst.ApplyForCLI(context.Background(), "claude", "", []PluginDecl{{Source: pluginSource}})
		if len(res) > 0 && res[0].Status == PluginInstallStatusFailed {
			return fmt.Errorf("%s", res[0].Stderr)
		}
		// Fresh installs land disabled globally (issue #666); the writes below
		// flip the requested scope on.
		_ = m.setPluginEnabledAt("", pluginSource, false)
	}
	if t.Scope == "workspace" {
		root, err := m.workspaceDir(context.Background(), workspaceID)
		if err != nil {
			return err
		}
		return m.setPluginEnabledAt(root, pluginSource, true)
	}
	return m.setPluginEnabledAt("", pluginSource, true)
}

func (m *SkillManager) copyBuiltinTo(name, dst string) error {
	src := path.Join(builtinSkillsRoot, name)
	if err := copyEmbeddedDir(builtinSkillsFS, src, dst); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dst, niuniuManagedSkillMarker), []byte("managed_by: niuniu\n"), 0o644)
}

// workspaceDir resolves a workspace's root dir through the owner path helper.
func (m *SkillManager) workspaceDir(ctx context.Context, workspaceID int64) (string, error) {
	if workspaceID <= 0 {
		return "", fmt.Errorf("workspace_id is required for workspace-scope targets")
	}
	ws, err := m.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return "", fmt.Errorf("load workspace: %w", err)
	}
	return OwnerRef{Type: ws.OwnerType, ID: ws.OwnerID}.WorkspacePath(m.dataDir, ws.ID), nil
}

// targetSkillsDir resolves the absolute skills directory for a target.
func (m *SkillManager) targetSkillsDir(ctx context.Context, t SkillTarget, workspaceID int64) (string, error) {
	switch t.Scope {
	case "global":
		return m.globalSkillsRoot(t.Agent)
	case "workspace":
		wsDir, err := m.workspaceDir(ctx, workspaceID)
		if err != nil {
			return "", err
		}
		agent := t.Agent
		if agent == "" {
			ws, werr := m.q.GetWorkspace(ctx, workspaceID)
			if werr != nil {
				return "", fmt.Errorf("load workspace: %w", werr)
			}
			agent = ws.CliType
		}
		return filepath.Join(wsDir, skillDirFor(agent)), nil
	default:
		return "", fmt.Errorf("invalid scope %q", t.Scope)
	}
}

func (m *SkillManager) validateTarget(t SkillTarget, workspaceID int64) error {
	if t.Agent == "" {
		return fmt.Errorf("agent is required")
	}
	known := false
	for _, a := range SkillAgents {
		if a == t.Agent {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("unknown agent %q", t.Agent)
	}
	if t.Scope == "workspace" && workspaceID <= 0 {
		return fmt.Errorf("workspace_id is required for workspace-scope targets")
	}
	if t.Scope != "global" && t.Scope != "workspace" {
		return fmt.Errorf("invalid scope %q", t.Scope)
	}
	return nil
}

// globalSkillsRoot returns <home>/<agent skills dir> for the global scope.
func (m *SkillManager) globalSkillsRoot(agent string) (string, error) {
	home, err := m.homeDir()
	if err != nil {
		return "", fmt.Errorf("user home dir: %w", err)
	}
	return filepath.Join(home, skillDirFor(agent)), nil
}

// settingsPath returns the settings.json path for an enable scope. root == ""
// means the user home (global scope).
func (m *SkillManager) settingsPath(root string) (string, error) {
	if root == "" {
		home, err := m.homeDir()
		if err != nil {
			return "", fmt.Errorf("user home dir: %w", err)
		}
		root = home
	}
	return filepath.Join(root, ".claude", "settings.json"), nil
}

// pluginEnabledAt reports whether enabledPlugins[source] is true in the
// settings.json under root ("" = user home). Best-effort: any read/parse
// failure simply reports not-enabled.
func (m *SkillManager) pluginEnabledAt(root, source string) bool {
	p, err := m.settingsPath(root)
	if err != nil {
		return false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	var doc struct {
		EnabledPlugins map[string]bool `json:"enabledPlugins"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return false
	}
	return doc.EnabledPlugins[source]
}

// setPluginEnabledAt merges enabledPlugins[source]=enabled into the
// settings.json under root ("" = user home), preserving every other key. The
// write is atomic (tmp + rename), mirroring MCPConfigGenerator's settings
// writer.
func (m *SkillManager) setPluginEnabledAt(root, source string, enabled bool) error {
	p, err := m.settingsPath(root)
	if err != nil {
		return err
	}
	return setPluginEnabledInSettings(p, source, enabled)
}

// setPluginEnabledInHomeSettings is the standalone variant for callers without
// a SkillManager (the scene projector default-disabling freshly installed
// skill plugins): writes enabledPlugins[source]=enabled into the user home's
// ~/.claude/settings.json.
func setPluginEnabledInHomeSettings(source string, enabled bool) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("user home dir: %w", err)
	}
	return setPluginEnabledInSettings(filepath.Join(home, ".claude", "settings.json"), source, enabled)
}

// setPluginEnabledInSettings merges enabledPlugins[source]=enabled into the
// settings.json at path, preserving every other key. Atomic tmp+rename write.
func setPluginEnabledInSettings(p, source string, enabled bool) error {
	if !isMarketplacePluginID(source) {
		return fmt.Errorf("%q is not a marketplace plugin id", source)
	}
	doc := map[string]any{}
	if b, err := os.ReadFile(p); err == nil {
		if err := json.Unmarshal(b, &doc); err != nil {
			return fmt.Errorf("parse settings.json: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read settings.json: %w", err)
	}
	ep, _ := doc["enabledPlugins"].(map[string]any)
	if ep == nil {
		ep = map[string]any{}
	}
	if v, ok := ep[source].(bool); ok && v == enabled {
		return nil // already in the desired state
	}
	ep[source] = enabled
	doc["enabledPlugins"] = ep
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp-niuniu-skills"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// builtinDiffers reports whether the installed copy at dir differs from the
// embedded payload (name+content tree hash). False on any read error - an
// unverifiable copy simply never offers an update.
func (m *SkillManager) builtinDiffers(name, dir string) bool {
	want, err := hashEmbeddedTree(builtinSkillsFS, path.Join(builtinSkillsRoot, name))
	if err != nil {
		return false
	}
	got, err := hashLocalTree(dir, nil)
	if err != nil {
		return false
	}
	return want != got
}

// hashEmbeddedTree hashes the file names + contents of srcRoot inside fsys,
// excluding the niuniu marker (it is stamped after copying).
func hashEmbeddedTree(fsys fs.FS, srcRoot string) (string, error) {
	h := sha256.New()
	err := fs.WalkDir(fsys, srcRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, srcRoot), "/")
		if rel == niuniuManagedSkillMarker {
			return nil
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		fmt.Fprintln(h, rel)
		_, err = h.Write(b)
		return err
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashLocalTree is hashEmbeddedTree's local-disk sibling.
func hashLocalTree(root string, skip func(rel string) bool) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == niuniuManagedSkillMarker || (skip != nil && skip(rel)) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		fmt.Fprintln(h, rel)
		_, err = h.Write(b)
		return err
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// SkillGlobalEnabled reports whether a vendored skill is currently ENABLED in
// an agent's GLOBAL skills dir (~/.<agent>/skills/<name>/SKILL.md exists).
// Scene materialization uses this for the global-first dedup: a scene skill
// that is already globally enabled needs no workspace-local copy.
func SkillGlobalEnabled(agent, name string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(home, skillDirFor(agent), name, "SKILL.md"))
	return err == nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
