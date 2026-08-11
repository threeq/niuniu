// Package service: scene projection — composes ordered scene layers into a
// single Projection that drives .mcp.json + CLAUDE.md fragments + asset
// imports + plugin install.
//
// Field-merge rules (spec §2.3):
//
//	MCP        — union by name (first-occurrence ordering)
//	Plugins    — union by source; later layer's Ref/Optional overrides
//	Assets.*   — union by slug; later layer's payload overrides
//	Prompts    — union by id; later layer's title/body overrides
//	Credentials— union by alias; non-optional wins regardless of order
package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// LayerOrigin tags one layer's contribution to a Projection. SceneID == -1
// is the special sentinel for the implicit base layer (workspace's pre-scene
// local config).
type LayerOrigin int64

const BaseLayerOrigin LayerOrigin = -1

// Projection is the materialised result of composing ordered scene layers.
// All "by-key" lookups use the same first-seen vs override semantics
// documented at the package level.
type Projection struct {
	MCPNames []string `json:"mcp"`
	// MCPConfigs holds inline ".mcp.json" server entries keyed by MCP name,
	// for scene-authored MCP servers not present in the Claude registry. Names
	// here also appear in MCPNames; the inline entry is written verbatim and
	// takes precedence over any registry resolution of the same name.
	MCPConfigs          map[string]map[string]any `json:"mcp_configs,omitempty"`
	Plugins             []PluginDecl              `json:"plugins"`
	// Skills is the union (by name) of vendored Claude skills the composed
	// scenes project into <wsDir>/.claude/skills/. Materialized as local file
	// copies on Apply (see materializeWorkspaceSkills) — never a network install.
	Skills              []SkillDecl          `json:"skills,omitempty"`
	Assets              SceneAssets          `json:"assets"`
	Prompts             []PromptFragment     `json:"prompts"`
	RequiredCredentials []RequiredCredential `json:"required_credentials"`
	RequiredDataSources []RequiredDataSource `json:"required_data_sources,omitempty"`
	// DisableToolGroups is the union (across layers) of niuniu built-in MCP tool
	// groups to hide from the agent (projected as niuniu-mcp --disable-tool-groups).
	DisableToolGroups []string `json:"disable_tool_groups,omitempty"`
	// EnableToolGroups is the union (across layers) of opt-in niuniu built-in MCP
	// tool groups to turn on (projected as niuniu-mcp --enable-tool-groups).
	EnableToolGroups []string `json:"enable_tool_groups,omitempty"`
	// Provenance maps "kind:key" → ordered list of LayerOrigin values that
	// contributed to that key. Used by the UI to show "from scene X (overrode
	// scene Y)" badges. Key examples: "mcp:memory", "plugin:github:foo/bar",
	// "prompt:support-tone", "env_preset:slack-default", "cred:slack-bot".
	Provenance map[string][]LayerOrigin `json:"provenance"`
}

// NewProjection returns an empty Projection ready for MergeFrom calls.
func NewProjection() *Projection {
	return &Projection{
		Provenance: map[string][]LayerOrigin{},
	}
}

// MergeFrom folds `def` into `p`, tagging origin info for UI badges.
// Callers invoke this for each layer in position-ascending order; the first
// invocation is typically the implicit base (origin=BaseLayerOrigin).
func (p *Projection) MergeFrom(def *SceneDefinition, origin LayerOrigin) {
	if def == nil {
		return
	}
	p.mergeMCP(def.MCP, origin)
	p.mergePlugins(def.Plugins, origin)
	p.mergeSkills(def.Skills, origin)
	p.mergeAssets(&def.Assets, origin)
	p.mergePrompts(def.Prompts, origin)
	p.mergeCredentials(def.RequiredCredentials, origin)
	p.mergeDataSources(def.RequiredDataSources, origin)
	p.mergeDisableToolGroups(def.DisableToolGroups, origin)
	p.mergeEnableToolGroups(def.EnableToolGroups, origin)
}

// mergeDisableToolGroups unions a layer's disabled tool groups into the
// projection (a group disabled by any layer stays disabled).
func (p *Projection) mergeDisableToolGroups(in []string, origin LayerOrigin) {
	seen := map[string]bool{}
	for _, g := range p.DisableToolGroups {
		seen[g] = true
	}
	for _, g := range in {
		if g == "" || seen[g] {
			continue
		}
		p.DisableToolGroups = append(p.DisableToolGroups, g)
		seen[g] = true
		p.appendOrigin("disable_tool_group:"+g, origin)
	}
}

// mergeEnableToolGroups unions a layer's opt-in enabled tool groups into the
// projection (a group enabled by any layer stays enabled).
func (p *Projection) mergeEnableToolGroups(in []string, origin LayerOrigin) {
	seen := map[string]bool{}
	for _, g := range p.EnableToolGroups {
		seen[g] = true
	}
	for _, g := range in {
		if g == "" || seen[g] {
			continue
		}
		p.EnableToolGroups = append(p.EnableToolGroups, g)
		seen[g] = true
		p.appendOrigin("enable_tool_group:"+g, origin)
	}
}

func (p *Projection) mergeMCP(in []MCPDecl, origin LayerOrigin) {
	idx := map[string]bool{}
	for _, n := range p.MCPNames {
		idx[n] = true
	}
	for _, m := range in {
		if !idx[m.Name] {
			p.MCPNames = append(p.MCPNames, m.Name)
			idx[m.Name] = true
		}
		// Inline config: later layer wins.
		if len(m.Config) > 0 {
			if p.MCPConfigs == nil {
				p.MCPConfigs = map[string]map[string]any{}
			}
			p.MCPConfigs[m.Name] = m.Config
		}
		p.appendOrigin("mcp:"+m.Name, origin)
	}
}

func (p *Projection) mergePlugins(in []PluginDecl, origin LayerOrigin) {
	for _, plug := range in {
		found := -1
		for i, existing := range p.Plugins {
			if existing.Source == plug.Source {
				found = i
				break
			}
		}
		if found >= 0 {
			// Later layer wins for Ref + Optional.
			p.Plugins[found].Ref = plug.Ref
			p.Plugins[found].Optional = plug.Optional
		} else {
			p.Plugins = append(p.Plugins, plug)
		}
		p.appendOrigin("plugin:"+plug.Source, origin)
	}
}

// mergeSkills unions a layer's skill declarations by name (first-seen ordering);
// a later layer overrides the earlier Optional flag for the same name.
func (p *Projection) mergeSkills(in []SkillDecl, origin LayerOrigin) {
	for _, sk := range in {
		found := -1
		for i, existing := range p.Skills {
			if existing.Name == sk.Name {
				found = i
				break
			}
		}
		if found >= 0 {
			p.Skills[found].Optional = sk.Optional
		} else {
			p.Skills = append(p.Skills, sk)
		}
		p.appendOrigin("skill:"+sk.Name, origin)
	}
}

func (p *Projection) mergeAssets(in *SceneAssets, origin LayerOrigin) {
	p.Assets.EnvPresets = mergeAssetSlice(p.Assets.EnvPresets, in.EnvPresets,
		func(x EnvPresetAsset) string { return x.Slug },
		func(slug string) { p.appendOrigin("env_preset:"+slug, origin) })
	p.Assets.Providers = mergeAssetSlice(p.Assets.Providers, in.Providers,
		func(x ProviderAsset) string { return x.Name },
		func(name string) { p.appendOrigin("provider:"+name, origin) })
	p.Assets.QuickActions = mergeAssetSlice(p.Assets.QuickActions, in.QuickActions,
		func(x QuickActionAsset) string { return x.Slug },
		func(slug string) { p.appendOrigin("quick_action:"+slug, origin) })
	p.Assets.ProjectTemplates = mergeAssetSlice(p.Assets.ProjectTemplates, in.ProjectTemplates,
		func(x ProjectTemplateAsset) string { return x.Slug },
		func(slug string) { p.appendOrigin("project_template:"+slug, origin) })
	p.Assets.HarnessSpecs = mergeAssetSlice(p.Assets.HarnessSpecs, in.HarnessSpecs,
		func(x HarnessSpecAsset) string { return x.Slug },
		func(slug string) { p.appendOrigin("harness_spec:"+slug, origin) })
	p.Assets.Agents = mergeAssetSlice(p.Assets.Agents, in.Agents,
		func(x AgentAsset) string { return x.Name },
		func(name string) { p.appendOrigin("agent:"+name, origin) })
}

// mergeAssetSlice unions by slug; later layer's payload wins. The recordOrigin
// callback fires AFTER the merge so the kind-prefixed key is attributed
// correctly.
func mergeAssetSlice[T any](existing, incoming []T, slugOf func(T) string, recordOrigin func(slug string)) []T {
	for _, x := range incoming {
		slug := slugOf(x)
		found := -1
		for i := range existing {
			if slugOf(existing[i]) == slug {
				found = i
				break
			}
		}
		if found >= 0 {
			existing[found] = x
		} else {
			existing = append(existing, x)
		}
		recordOrigin(slug)
	}
	return existing
}

func (p *Projection) mergePrompts(in []PromptFragment, origin LayerOrigin) {
	for _, pr := range in {
		found := -1
		for i, existing := range p.Prompts {
			if existing.ID == pr.ID {
				found = i
				break
			}
		}
		if found >= 0 {
			p.Prompts[found] = pr
		} else {
			p.Prompts = append(p.Prompts, pr)
		}
		p.appendOrigin("prompt:"+pr.ID, origin)
	}
}

func (p *Projection) mergeCredentials(in []RequiredCredential, origin LayerOrigin) {
	for _, rc := range in {
		found := -1
		for i, existing := range p.RequiredCredentials {
			if existing.Alias == rc.Alias {
				found = i
				break
			}
		}
		if found >= 0 {
			// Non-optional wins: if either layer says required, it's required.
			if !rc.Optional {
				p.RequiredCredentials[found].Optional = false
			}
			// Provider and Purpose: later layer wins (assume more recent context).
			p.RequiredCredentials[found].Provider = rc.Provider
			if rc.Purpose != "" {
				p.RequiredCredentials[found].Purpose = rc.Purpose
			}
		} else {
			p.RequiredCredentials = append(p.RequiredCredentials, rc)
		}
		p.appendOrigin("cred:"+rc.Alias, origin)
	}
}

// mergeDataSources unions required data sources by Name; non-optional wins, and
// the later layer's Kind/Purpose override.
func (p *Projection) mergeDataSources(in []RequiredDataSource, origin LayerOrigin) {
	for _, ds := range in {
		if ds.Name == "" {
			continue
		}
		found := -1
		for i, existing := range p.RequiredDataSources {
			if existing.Name == ds.Name {
				found = i
				break
			}
		}
		if found >= 0 {
			if !ds.Optional {
				p.RequiredDataSources[found].Optional = false
			}
			if ds.Kind != "" {
				p.RequiredDataSources[found].Kind = ds.Kind
			}
			if ds.Purpose != "" {
				p.RequiredDataSources[found].Purpose = ds.Purpose
			}
		} else {
			p.RequiredDataSources = append(p.RequiredDataSources, ds)
		}
		p.appendOrigin("data_source:"+ds.Name, origin)
	}
}

func (p *Projection) appendOrigin(key string, origin LayerOrigin) {
	for _, o := range p.Provenance[key] {
		if o == origin {
			return
		}
	}
	p.Provenance[key] = append(p.Provenance[key], origin)
}

// Digest returns a stable hash of the projection's process-relevant fields
// (MCP + plugins). Used by SceneProjector.Apply to determine restart_required
// — only changes to fields that affect the running agent count, not e.g.
// CLAUDE.md text changes.
func (p *Projection) Digest() string {
	type processView struct {
		MCP        []string                  `json:"mcp"`
		MCPConfigs map[string]map[string]any `json:"mcp_configs,omitempty"`
		Plugins    []PluginDecl              `json:"plugins"`
	}
	b, _ := json.Marshal(processView{MCP: p.MCPNames, MCPConfigs: p.MCPConfigs, Plugins: p.Plugins})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// FullDigest hashes the entire projection (file-affecting fields too). Used
// to skip Recompute work when nothing changed at all.
func (p *Projection) FullDigest() string {
	b, _ := json.Marshal(p)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// RenderCLAUDEMdFragment builds the begin/end-tagged markdown block that
// SceneProjector.Apply injects into <wsPath>/CLAUDE.md. Returns "" when
// the projection has no prompts (caller skips the write in that case).
const (
	ClaudeMdFragmentBegin = "<!-- BEGIN niuniu scene fragments -->"
	ClaudeMdFragmentEnd   = "<!-- END niuniu scene fragments -->"
)

func (p *Projection) RenderCLAUDEMdFragment() string {
	if len(p.Prompts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(ClaudeMdFragmentBegin)
	b.WriteByte('\n')
	b.WriteString("<!-- DO NOT EDIT this block; managed by niuniu scene projector -->\n\n")
	for i, pr := range p.Prompts {
		fmt.Fprintf(&b, "## %s\n\n", pr.Title)
		b.WriteString(pr.Body)
		if !strings.HasSuffix(pr.Body, "\n") {
			b.WriteByte('\n')
		}
		if i < len(p.Prompts)-1 {
			b.WriteString("\n---\n\n")
		} else {
			b.WriteByte('\n')
		}
	}
	b.WriteString(ClaudeMdFragmentEnd)
	b.WriteByte('\n')
	return b.String()
}

// ReplaceOrAppendCLAUDEMdBlock embeds `block` into `existing` by swapping
// content between begin/end markers if present; otherwise appends.
// Used by SceneProjector.Apply to write the workspace's CLAUDE.md.
func ReplaceOrAppendCLAUDEMdBlock(existing, block string) string {
	if block == "" {
		// No prompts → remove any existing block but keep user content.
		i := strings.Index(existing, ClaudeMdFragmentBegin)
		j := strings.Index(existing, ClaudeMdFragmentEnd)
		if i >= 0 && j > i {
			tail := existing[j+len(ClaudeMdFragmentEnd):]
			head := existing[:i]
			// Trim a trailing newline pair that was the separator.
			head = strings.TrimRight(head, "\n")
			if head != "" && tail != "" {
				return head + "\n\n" + strings.TrimLeft(tail, "\n")
			}
			return head + tail
		}
		return existing
	}
	i := strings.Index(existing, ClaudeMdFragmentBegin)
	j := strings.Index(existing, ClaudeMdFragmentEnd)
	if i >= 0 && j > i {
		return existing[:i] + block + existing[j+len(ClaudeMdFragmentEnd)+1:] // +1 trailing newline
	}
	if existing == "" {
		return block
	}
	if !strings.HasSuffix(existing, "\n") {
		existing += "\n"
	}
	return existing + "\n" + block
}
