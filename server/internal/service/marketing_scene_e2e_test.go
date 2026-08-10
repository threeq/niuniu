package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// loadBuiltinSceneDef parses one embedded builtin scene YAML into a validated
// SceneDefinition, mirroring what SceneSeeder.seedOne does at boot. It exercises
// the real parse+validate path so a malformed marketing YAML fails the test.
func loadBuiltinSceneDef(t *testing.T, file string) (builtinSceneYAML, *SceneDefinition) {
	t.Helper()
	raw, err := builtinScenesFS.ReadFile("builtin_scenes/" + file)
	require.NoErrorf(t, err, "read embedded %s (run `make builtin-scenes-sync`)", file)
	var doc builtinSceneYAML
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	def := &SceneDefinition{
		MCP: doc.MCP, Plugins: doc.Plugins, Skills: doc.Skills, Assets: doc.Assets,
		Prompts: doc.Prompts, RequiredCredentials: doc.RequiredCredentials,
		DisableToolGroups: doc.DisableToolGroups, EnableToolGroups: doc.EnableToolGroups,
		KnowledgeBases: doc.KnowledgeBases, Match: doc.Match,
	}
	require.NoErrorf(t, ValidateSceneDefinition(def), "%s must be a valid scene", file)
	return doc, def
}

// TestMarketingScene_ContentMarketing_Projects is the end-to-end composition
// check for the 内容营销全流程 scene: parsing its builtin YAML and projecting it
// (the same MergeFrom path used at workspace-enable) yields a no_repo-friendly
// bundle with curated MCP (fetch), the site-audit skill, the 审校 gate template,
// and the optional cross-border data-source credentials — i.e. "curated MCP 就绪".
func TestMarketingScene_ContentMarketing_Projects(t *testing.T) {
	doc, def := loadBuiltinSceneDef(t, "content-marketing.yaml")
	assert.Equal(t, "content-marketing", doc.Slug)

	p := NewProjection()
	p.MergeFrom(def, BaseLayerOrigin)

	// Curated MCP ready.
	assert.Contains(t, p.MCPNames, "fetch")
	// Vendored technical-SEO skill materialized for the 发布/复盘 lanes.
	skillNames := make([]string, len(p.Skills))
	for i, s := range p.Skills {
		skillNames[i] = s.Name
	}
	assert.Contains(t, skillNames, "site-audit")

	// gate_specs template: the 营销文案审校门禁 ai_judge gate is carried as a
	// harness_spec asset, bound on the 审校 lane as a phase_exit gate.
	require.Len(t, p.Assets.HarnessSpecs, 1)
	gate := p.Assets.HarnessSpecs[0]
	assert.Equal(t, "copy-review-gate", gate.Slug)
	assert.Equal(t, "ai_judge", gate.Payload["kind"])
	assert.Equal(t, "phase_exit", gate.Payload["trigger_on"])

	// Phase-aligned quick actions cover the whole 需求→…→复盘 loop.
	qaSlugs := map[string]bool{}
	for _, qa := range p.Assets.QuickActions {
		qaSlugs[qa.Slug] = true
	}
	for _, want := range []string{"topic-keyword-research", "draft-copy", "editorial-review", "publish-checklist", "performance-recap"} {
		assert.Truef(t, qaSlugs[want], "quick action %q missing", want)
	}

	// Optional cross-border data sources declared (never carry secrets).
	credAliases := map[string]bool{}
	for _, c := range def.RequiredCredentials {
		credAliases[c.Alias] = true
		assert.Truef(t, c.Optional, "credential %q should be optional", c.Alias)
	}
	for _, want := range []string{"serp-api", "gsc", "ahrefs", "semrush"} {
		assert.Truef(t, credAliases[want], "credential %q missing", want)
	}

	// Single-agent no_repo focus: cross-agent + agent-facing harness tools hidden.
	assert.Contains(t, p.DisableToolGroups, "multi-agent")
	assert.Contains(t, p.DisableToolGroups, "harness")
}

// TestMarketingScene_SocialOps_Projects is the end-to-end composition check for
// the 社媒运营周报 scene: it projects to a no_repo bundle with fetch MCP, the
// weekly-report cron action, and the scheduling/notify guidance prompt that wires
// the scheduler + notify + imbot primitives.
func TestMarketingScene_SocialOps_Projects(t *testing.T) {
	doc, def := loadBuiltinSceneDef(t, "social-ops.yaml")
	assert.Equal(t, "social-ops", doc.Slug)

	p := NewProjection()
	p.MergeFrom(def, BaseLayerOrigin)

	assert.Contains(t, p.MCPNames, "fetch")

	qaSlugs := map[string]bool{}
	for _, qa := range p.Assets.QuickActions {
		qaSlugs[qa.Slug] = true
	}
	for _, want := range []string{"content-calendar", "post-scheduling", "metrics-collect", "weekly-report", "sentiment-digest"} {
		assert.Truef(t, qaSlugs[want], "quick action %q missing", want)
	}

	// The scheduling+notify guidance prompt is present (reuses scheduler/notify/imbot).
	promptIDs := map[string]bool{}
	for _, pr := range p.Prompts {
		promptIDs[pr.ID] = true
	}
	assert.True(t, promptIDs["social-ops-scheduling-notify"], "scheduling/notify guidance prompt missing")
}
