package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWechatMPScene_Projects is the end-to-end composition check for the
// 微信公众号运营 scene: parsing its builtin YAML and projecting it (the same
// MergeFrom path used at workspace-enable) yields a no_repo-friendly bundle with
// the official-API 公众号 MCP (auth via a credstore placeholder, never plaintext),
// the 发布前审校 gate template, the phase-aligned quick actions, and the
// scheduling/notify + compliance guidance prompts — i.e. "curated MCP 就绪".
func TestWechatMPScene_Projects(t *testing.T) {
	doc, def := loadBuiltinSceneDef(t, "wechat-mp.yaml")
	assert.Equal(t, "wechat-mp", doc.Slug)
	assert.Equal(t, "微信公众号运营", doc.DisplayName)

	p := NewProjection()
	p.MergeFrom(def, BaseLayerOrigin)

	// Curated official-API MCP ready.
	assert.Contains(t, p.MCPNames, "wechat-mp")

	// The 公众号 MCP: http transport pointed at the user's own endpoint, auth via a
	// credstore placeholder — the token is NEVER inlined in the scene definition.
	require.Len(t, doc.MCP, 1, "exactly one 公众号 MCP server")
	mcp := doc.MCP[0]
	assert.Equal(t, "wechat-mp", mcp.Name)
	require.NotNil(t, mcp.Config, "公众号 MCP must carry an inline config")
	assert.Equal(t, "http", mcp.Config["type"], "公众号 MCP is an http server")
	assert.NotEmpty(t, mcp.Config["url"], "公众号 MCP needs an endpoint url")
	headers, ok := mcp.Config["headers"].(map[string]any)
	require.True(t, ok, "公众号 MCP must declare auth headers")
	authz, _ := headers["Authorization"].(string)
	assert.Contains(t, authz, "${cred:wechat-mp.token}",
		"auth token must be a credstore placeholder, never plaintext")

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

	// Required credential declared (never carries a secret) and NOT optional — the
	// server can only project once the token is bound.
	require.Len(t, def.RequiredCredentials, 1)
	rc := def.RequiredCredentials[0]
	assert.Equal(t, "wechat-mp", rc.Alias)
	assert.Equal(t, "wechat-mp", rc.Provider)
	assert.False(t, rc.Optional, "the 公众号 MCP token is required for the server to project")

	// Single-agent no_repo focus: cross-agent + agent-facing harness tools hidden.
	assert.Contains(t, p.DisableToolGroups, "multi-agent")
	assert.Contains(t, p.DisableToolGroups, "harness")
}

// TestWechatMPScene_CredentialInjection proves the projection-time contract the
// scene depends on: the ${cred:wechat-mp.token} placeholder in the auth header is
// resolved to the real decrypted value when the credential is bound, and — when
// it is missing — the alias is reported so the whole server is dropped (spec
// §4.2.4: never write a half-filled auth). Mirrors how resolveProjectionCredentials
// rewrites the "headers" sub-map, exercised here as the pure resolveCredEnv.
func TestWechatMPScene_CredentialInjection(t *testing.T) {
	doc, _ := loadBuiltinSceneDef(t, "wechat-mp.yaml")
	headers, ok := doc.MCP[0].Config["headers"].(map[string]any)
	require.True(t, ok)
	authz, _ := headers["Authorization"].(string)
	require.Contains(t, authz, "${cred:wechat-mp.token}")

	sub := map[string]string{"Authorization": authz}

	// Bound: placeholder resolves to the real token, which never lived in the scene.
	out, missing := resolveCredEnv(sub, staticLookup(map[string]string{
		"wechat-mp.token": "s3cr3t-mp-token",
	}))
	require.Empty(t, missing)
	assert.Equal(t, "Bearer s3cr3t-mp-token", out["Authorization"])

	// Unbound: the alias is reported missing → caller drops the server, no leak.
	_, missing = resolveCredEnv(sub, staticLookup(map[string]string{}))
	assert.Equal(t, []string{"wechat-mp"}, missing)
}
