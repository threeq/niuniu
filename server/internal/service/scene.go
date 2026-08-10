// Package service: scene types and the SceneService.
//
// A Scene is a niuniu owner-scoped, composable bundle of MCP servers, Claude
// plugins, niuniu assets (env_presets / quick_actions / project_templates /
// harness_specs / agents), CLAUDE.md prompt fragments, and required-credential
// declarations. See docs/superpowers/specs/2026-05-17-scene-based-mcp-plugin-management-design.md.
package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// SceneDefinition is the canonical in-memory representation of a scene's
// declaration. It is what gets persisted in scenes.definition (JSON-encoded)
// and what SceneSeeder produces from YAML files in docs/scenes/builtin/.
//
// Field semantics — see spec §2.1 / §2.3:
//   - MCP/Plugins lists: union by name/source at composition time
//   - Assets: union by slug, later layer payload wins
//   - Prompts: union by id, later layer body wins
//   - RequiredCredentials: union by alias; any non-optional declaration makes
//     the alias non-optional for the projected workspace
type SceneDefinition struct {
	MCP                 []MCPDecl            `json:"mcp"`
	Plugins             []PluginDecl         `json:"plugins"`
	// Skills declares vendored (in-repo) Claude skills the scene projects into
	// the workspace's <wsDir>/.claude/skills/<name>/ directory. Unlike Plugins
	// (which run `claude plugin install` and are gated behind an explicit SPA
	// click), skills are pure local file copies — materialized automatically on
	// scene-enable, like agents — so no network/CLI install is ever spawned.
	// The payloads ship embedded in the binary (builtin_skills/, mirrored from
	// docs/scenes/skills/ via `make builtin-skills-sync`).
	Skills              []SkillDecl          `json:"skills,omitempty"`
	Assets              SceneAssets          `json:"assets"`
	Prompts             []PromptFragment     `json:"prompts"`
	RequiredCredentials []RequiredCredential `json:"required_credentials"`
	// RequiredDataSources declares the niuniu data sources (数据源, managed under
	// Settings / Integrations) the scene needs, referenced by per-owner name —
	// the same model as a project's external sources, but for the dataconn
	// connectors the agent reaches via run_data_query. Bound at the workspace's
	// owner level, never embedding secrets in the scene.
	RequiredDataSources []RequiredDataSource `json:"required_data_sources,omitempty"`
	// DisableToolGroups hides whole groups of niuniu built-in MCP tools from the
	// agent in this scene (projected as the niuniu-mcp `--disable-tool-groups`
	// flag). Use it to keep a scene's toolset focused — e.g. office scenes hide
	// the "multi-agent" (blackboard/inbox) and "harness" (gate/pre-commit) groups
	// a single-agent, no-repo assistant never needs. Group names are defined in
	// cmd/niuniu-mcp; unknown names are ignored.
	DisableToolGroups []string   `json:"disable_tool_groups,omitempty"`
	// EnableToolGroups turns ON opt-in niuniu built-in MCP tool groups that are
	// OFF by default (projected as the niuniu-mcp `--enable-tool-groups` flag).
	// Used for privacy-sensitive tools that must never appear unless a scene
	// explicitly opts in — e.g. "browser-history" (read_browser_history) for the
	// info-radar scene. Group names are defined in cmd/niuniu-mcp.
	EnableToolGroups []string `json:"enable_tool_groups,omitempty"`
	// KnowledgeBases declares the knowledge bases (KB) this scene expects the
	// workspace to have access to, referenced by the KB's per-owner Name. It is
	// declarative provenance, not a binding action: actual visibility is granted
	// by binding the KB to the project (kb_bindings) under the owner's KB
	// management, and an agent reaches the bound KBs via the kb_search / kb_list
	// MCP tools. Listing a KB here documents the dependency and lets the SPA
	// surface "this scene uses KB X" without embedding any KB content or secrets.
	KnowledgeBases []SceneKBRef `json:"knowledge_bases,omitempty"`
	Match          MatchRules   `json:"match"`
}

// SceneKBRef references a knowledge base by its per-owner Name. The KB itself
// (source, documents, index) lives under the owner; the scene only names the
// dependency so it can be checked/surfaced at workspace-enable time.
type SceneKBRef struct {
	Name     string `json:"name" yaml:"name"`
	Purpose  string `json:"purpose,omitempty" yaml:"purpose,omitempty"`
	Optional bool   `json:"optional,omitempty" yaml:"optional,omitempty"`
}

// RequiredDataSource references a niuniu data source by its per-owner Name.
// Kind (mysql/postgres/mongo/...) is a display/validation hint carried with the
// scene; the actual connection + secrets live in the owner's data_sources row.
type RequiredDataSource struct {
	Name     string `json:"name" yaml:"name"`
	Kind     string `json:"kind,omitempty" yaml:"kind,omitempty"`
	Purpose  string `json:"purpose,omitempty" yaml:"purpose,omitempty"`
	Optional bool   `json:"optional,omitempty" yaml:"optional,omitempty"`
}

// MCPDecl declares an MCP server for a scene. When Config is empty it is a
// reference by Name: the actual command/args are resolved at .mcp.json
// generation time via ClaudeMCPRegistry (the server must already be installed
// in the Claude config). When Config is set it is an inline ".mcp.json" server
// entry (e.g. {"command":"npx","args":[...],"env":{...}} or {"type":"http",
// "url":"..."}) authored by the user and written verbatim into the workspace's
// .mcp.json on projection — no registry lookup and no manual file editing.
type MCPDecl struct {
	Name   string         `json:"name" yaml:"name"`
	Config map[string]any `json:"config,omitempty" yaml:"config,omitempty"`
}

// PluginDecl is the Claude Code plugin declaration. Source must match the
// whitelist in isAllowedPluginSource: a marketplace id (name@marketplace, e.g.
// playwright@claude-plugins-official), github:owner/repo,
// https://github.com/owner/repo, or file:///abs.
type PluginDecl struct {
	Source   string `json:"source" yaml:"source"`
	Ref      string `json:"ref,omitempty" yaml:"ref,omitempty"`
	Optional bool   `json:"optional,omitempty" yaml:"optional,omitempty"`
}

// SkillDecl references a vendored Claude skill by its directory name under
// docs/scenes/skills/ (mirrored into builtin_skills/). Name must be a safe slug
// — it becomes the <wsDir>/.claude/skills/<Name>/ directory on projection.
type SkillDecl struct {
	Name     string `json:"name" yaml:"name"`
	Optional bool   `json:"optional,omitempty" yaml:"optional,omitempty"`
}

// SceneAssets bundles niuniu's reusable resources by kind. Each entry is
// inlined into the scene definition; importAssets in SceneProjector creates
// owner-scoped rows in the target tables, idempotent via (owner, slug).
type SceneAssets struct {
	EnvPresets       []EnvPresetAsset       `json:"env_presets,omitempty" yaml:"env_presets,omitempty"`
	ProjectTemplates []ProjectTemplateAsset `json:"project_templates,omitempty" yaml:"project_templates,omitempty"`
	QuickActions     []QuickActionAsset     `json:"quick_actions,omitempty" yaml:"quick_actions,omitempty"`
	HarnessSpecs     []HarnessSpecAsset     `json:"harness_specs,omitempty" yaml:"harness_specs,omitempty"`
	Agents           []AgentAsset           `json:"agents,omitempty" yaml:"agents,omitempty"`
}

type EnvPresetAsset struct {
	Slug        string            `json:"slug" yaml:"slug"`
	Name        string            `json:"name,omitempty" yaml:"name,omitempty"`
	Description string            `json:"description,omitempty" yaml:"description,omitempty"`
	Env         map[string]string `json:"env" yaml:"env"`
}

type ProjectTemplateAsset struct {
	Slug    string         `json:"slug" yaml:"slug"`
	Name    string         `json:"name,omitempty" yaml:"name,omitempty"`
	Payload map[string]any `json:"payload" yaml:"payload"`
}

type QuickActionAsset struct {
	Slug   string `json:"slug" yaml:"slug"`
	Label  string `json:"label" yaml:"label"`
	Prompt string `json:"prompt" yaml:"prompt"`
}

type HarnessSpecAsset struct {
	Slug    string         `json:"slug" yaml:"slug"`
	Name    string         `json:"name,omitempty" yaml:"name,omitempty"`
	Payload map[string]any `json:"payload" yaml:"payload"`
}

// AgentAsset references an existing system agent (managed on the Agents page)
// by its unique name. Scenes REFERENCE agents — they do not author them. On
// projection the referenced agent's markdown is copied into the workspace's
// Claude agent directory (<workspace>/.claude/agents/<name>.md). Codex
// workspaces have no subagent directory, so agent refs are skipped there.
type AgentAsset struct {
	Name string `json:"name" yaml:"name"`
}

// PromptFragment is one named block injected into the workspace's CLAUDE.md
// between begin/end markers. id is the stable identifier for dedup across
// layers; body is markdown.
type PromptFragment struct {
	ID    string `json:"id" yaml:"id"`
	Title string `json:"title" yaml:"title"`
	Body  string `json:"body" yaml:"body"`
}

// RequiredCredential declares a dependency on a user-bound credential. The
// scene NEVER carries a credential value; SceneProjector checks bindings at
// apply time and lists unbound aliases as MissingCredential entries.
type RequiredCredential struct {
	Alias    string `json:"alias" yaml:"alias"`
	Provider string `json:"provider" yaml:"provider"`
	Purpose  string `json:"purpose,omitempty" yaml:"purpose,omitempty"`
	Optional bool   `json:"optional,omitempty" yaml:"optional,omitempty"`
}

// MatchRules drives recommendation scoring. SceneMatcher evaluates each rule
// against the workspace's WorkspaceFeatures and sums matched weights atop
// BaseWeight.
type MatchRules struct {
	BaseWeight int         `json:"base_weight,omitempty" yaml:"base_weight,omitempty"`
	Rules      []MatchRule `json:"rules,omitempty" yaml:"rules,omitempty"`
}

type MatchRule struct {
	Signal string         `json:"signal" yaml:"signal"`
	Args   map[string]any `json:"args,omitempty" yaml:"args,omitempty"`
	Weight int            `json:"weight" yaml:"weight"`
}

// ValidateSceneDefinition rejects malformed payloads. Catches:
//   - Duplicate MCP names within a single scene (otherwise harmless but signals
//     authoring bug)
//   - Plugin sources outside the whitelist (avoid shell injection vectors)
//   - Required-credential entries missing alias or provider
//   - Asset entries with empty slug (the import path requires slug for idempotency)
func ValidateSceneDefinition(def *SceneDefinition) error {
	if def == nil {
		return errors.New("nil scene definition")
	}
	seenMCP := map[string]bool{}
	for _, m := range def.MCP {
		if m.Name == "" {
			return errors.New("mcp entry missing name")
		}
		if seenMCP[m.Name] {
			return fmt.Errorf("duplicate mcp name: %s", m.Name)
		}
		seenMCP[m.Name] = true
	}
	for _, p := range def.Plugins {
		if !isAllowedPluginSource(p.Source) {
			return fmt.Errorf("plugin source not allowed: %s", p.Source)
		}
	}
	seenSkill := map[string]bool{}
	for _, sk := range def.Skills {
		if !isSafeSkillName(sk.Name) {
			return fmt.Errorf("skill name not allowed: %q", sk.Name)
		}
		if seenSkill[sk.Name] {
			return fmt.Errorf("duplicate skill name: %s", sk.Name)
		}
		seenSkill[sk.Name] = true
	}
	for _, c := range def.RequiredCredentials {
		if c.Alias == "" || c.Provider == "" {
			return errors.New("required_credentials entry missing alias or provider")
		}
	}
	if err := validateAssetSlugs(&def.Assets); err != nil {
		return err
	}
	for _, pr := range def.Prompts {
		if pr.ID == "" {
			return errors.New("prompt fragment missing id")
		}
	}
	return nil
}

func validateAssetSlugs(a *SceneAssets) error {
	check := func(kind string, slugs []string) error {
		seen := map[string]bool{}
		for _, s := range slugs {
			if s == "" {
				return fmt.Errorf("%s asset missing slug", kind)
			}
			if seen[s] {
				return fmt.Errorf("duplicate %s asset slug: %s", kind, s)
			}
			seen[s] = true
		}
		return nil
	}
	envSlugs := make([]string, len(a.EnvPresets))
	for i, x := range a.EnvPresets {
		envSlugs[i] = x.Slug
	}
	if err := check("env_preset", envSlugs); err != nil {
		return err
	}
	qaSlugs := make([]string, len(a.QuickActions))
	for i, x := range a.QuickActions {
		qaSlugs[i] = x.Slug
	}
	if err := check("quick_action", qaSlugs); err != nil {
		return err
	}
	ptSlugs := make([]string, len(a.ProjectTemplates))
	for i, x := range a.ProjectTemplates {
		ptSlugs[i] = x.Slug
	}
	if err := check("project_template", ptSlugs); err != nil {
		return err
	}
	hsSlugs := make([]string, len(a.HarnessSpecs))
	for i, x := range a.HarnessSpecs {
		hsSlugs[i] = x.Slug
	}
	if err := check("harness_spec", hsSlugs); err != nil {
		return err
	}
	agNames := make([]string, len(a.Agents))
	for i, x := range a.Agents {
		agNames[i] = x.Name
	}
	return check("agent", agNames)
}

func isAllowedPluginSource(s string) bool {
	if s == "" {
		return false
	}
	switch {
	case strings.HasPrefix(s, "github:"),
		strings.HasPrefix(s, "https://github.com/"),
		strings.HasPrefix(s, "file:///"):
		return true
	}
	// name@marketplace (e.g. playwright@claude-plugins-official) — the canonical
	// Claude Code plugin id consumed by `claude plugin install <name@marketplace>`.
	if at := strings.IndexByte(s, '@'); at > 0 && at < len(s)-1 {
		if isPluginToken(s[:at]) && isPluginToken(s[at+1:]) {
			return true
		}
	}
	return false
}

// isSafeSkillName reports whether s is usable as a .claude/skills/<s>/ directory
// name: a non-empty slug of ASCII alphanumerics plus '-', '_', '.', with no path
// separators and no ".."/"."-only forms (guards against path traversal when the
// name is joined onto the workspace skills dir).
func isSafeSkillName(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// isPluginToken reports whether s is a valid plugin-id / marketplace token:
// non-empty, ASCII alphanumerics plus '-', '_', '.'.
func isPluginToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// HashDefinition computes the content hash used by SceneSeeder to detect
// changes between builtin YAML versions. Canonicalizes by re-marshalling
// through encoding/json (key order stable per Go runtime in 1.12+).
func HashDefinition(def *SceneDefinition) string {
	b, _ := json.Marshal(def)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// SlugifyForAsset normalizes a string for use as an asset slug when
// back-filling existing rows (`name` / `label` → `slug`). Lowercase, replace
// non-alphanumeric with '-', collapse runs, trim.
func SlugifyForAsset(s string) string {
	var b strings.Builder
	prevDash := true
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "untitled"
	}
	return out
}
