package sceneenv

import (
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
)

// codex 走 Responses API：provider 的 base_urls 用 "codex" 键携带（可能不同
// 的）Responses 端点；缺省回退 "openai" 键。展开结果必须换成 codex 端点。
func TestExpandProvider_CodexPrefersCodexBaseURL(t *testing.T) {
	p := store.EnvProvider{
		BaseUrls: `{"openai":"https://x.example/chat/v4","codex":"https://x.example/api/v1"}`,
		ApiKey:   "k",
		Model:    "m",
	}
	env := ExpandProvider(p, "codex", nil, true)
	require.Equal(t, "https://x.example/api/v1", env["OPENAI_BASE_URL"])
	require.Equal(t, "k", env["OPENAI_API_KEY"])

	// 无 codex 键：回退 openai URL。
	p2 := store.EnvProvider{BaseUrls: `{"openai":"https://x.example/chat/v4"}`, ApiKey: "k"}
	env2 := ExpandProvider(p2, "codex", nil, true)
	require.Equal(t, "https://x.example/chat/v4", env2["OPENAI_BASE_URL"])

	// claude 不受 codex 键影响（anthropic 协议）。
	p3 := store.EnvProvider{
		BaseUrls: `{"anthropic":"https://x.example/anthropic","codex":"https://x.example/api/v1"}`,
		ApiKey:   "k",
	}
	env3 := ExpandProvider(p3, "", nil, true)
	require.Equal(t, "https://x.example/anthropic", env3["ANTHROPIC_BASE_URL"])
	require.NotContains(t, env3, "OPENAI_BASE_URL")
}

// TestExpandProvider_CodexModelFallback：codex_model 设置时优先，缺省回退
// provider 默认模型（即 claude 侧配置的 model）。
func TestExpandProvider_CodexModelFallback(t *testing.T) {
	p := store.EnvProvider{
		BaseUrls: `{"openai":"https://x.example/api/v1"}`,
		ApiKey:   "k",
		Model:    "default-m",
	}
	env := ExpandProvider(p, "codex", nil, true)
	require.Equal(t, "default-m", env["NIUNIU_MODEL"])
	require.Equal(t, "default-m", env["OPENAI_MODEL"])

	p.CodexModel = "codex-m"
	env = ExpandProvider(p, "codex", nil, true)
	require.Equal(t, "codex-m", env["NIUNIU_MODEL"])
	require.Equal(t, "codex-m", env["OPENAI_MODEL"])
}
