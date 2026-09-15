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
