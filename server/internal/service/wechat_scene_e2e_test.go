package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWechatMPScene_Projects is the end-to-end composition check for the
// 微信公众号运营 scene: parsing its builtin YAML and projecting it (the same
// MergeFrom path used at workspace-enable) yields a no_repo-friendly bundle with
// NO external MCP dependency (the official API is called by the built-in Ops Bot),
// the 发布前审校 gate template, the phase-aligned quick actions, and the
// scheduling/notify + compliance guidance prompts.
func TestWechatMPScene_Projects(t *testing.T) {
	doc, def := loadBuiltinSceneDef(t, "wechat-mp.yaml")
	assert.Equal(t, "wechat-mp", doc.Slug)
	assert.Equal(t, "微信公众号运营", doc.DisplayName)

	p := NewProjection()
	p.MergeFrom(def, BaseLayerOrigin)

	// No external MCP: publishing runs through the built-in Ops Bot on the
	// official API, so the scene must not project a third-party server.
	assert.Empty(t, p.MCPNames, "the WeChat scene must not require an external MCP")
	assert.Empty(t, doc.MCP, "the WeChat scene declares no MCP servers")

	// gate_specs template: the 公众号发布前审校门禁 ai_judge gate is carried as a
	// harness_spec asset, bound on the 审校 lane as a phase_exit gate.
	require.Len(t, p.Assets.HarnessSpecs, 1)
	gate := p.Assets.HarnessSpecs[0]
	assert.Equal(t, "wechat-review-gate", gate.Slug)
	assert.Equal(t, "ai_judge", gate.Payload["kind"])
	assert.Equal(t, "phase_exit", gate.Payload["trigger_on"])

	// Phase-aligned quick actions cover the whole 选题→…→复盘 loop.
	qaSlugs := map[string]bool{}
	for _, qa := range p.Assets.QuickActions {
		qaSlugs[qa.Slug] = true
	}
	for _, want := range []string{"topic-angle", "draft-article", "format-layout", "pre-publish-review", "publish-schedule", "data-recap"} {
		assert.Truef(t, qaSlugs[want], "quick action %q missing", want)
	}

	// Compliance + scheduling/notify guidance prompts are present.
	promptIDs := map[string]bool{}
	for _, pr := range p.Prompts {
		promptIDs[pr.ID] = true
	}
	assert.True(t, promptIDs["wechat-mp-compliance"], "compliance red-line prompt missing")
	assert.True(t, promptIDs["wechat-mp-scheduling-notify"], "scheduling/notify guidance prompt missing")

	// No scene-level credential: AppID/AppSecret are account-level credentials
	// entered through the Ops Bot link, never declared in the scene.
	assert.Empty(t, def.RequiredCredentials, "the WeChat scene declares no credential slots")

	// Single-agent no_repo focus: cross-agent + agent-facing harness tools hidden.
	assert.Contains(t, p.DisableToolGroups, "multi-agent")
	assert.Contains(t, p.DisableToolGroups, "harness")
}
