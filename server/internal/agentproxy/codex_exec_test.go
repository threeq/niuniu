package agentproxy

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCodexProviderConfigArgs verifies the provider relay's codex -c overrides:
// no provider → nil (codex default backend); a base_url → a model_provider
// switch to Chat Completions (wire_api="chat") with the URL TOML-quoted.
func TestCodexProviderConfigArgs(t *testing.T) {
	require.Nil(t, codexProviderConfigArgs(""), "未解析出 provider 时不应产生覆盖")

	const base = "https://open.bigmodel.cn/api/coding/paas/v4"
	args := codexProviderConfigArgs(base)
	require.Len(t, args, 10) // 5 组 --config key=value
	joined := strings.Join(args, "\n")
	require.Contains(t, joined, `model_provider="niuniu-provider"`)
	require.Contains(t, joined, `base_url="`+base+`"`)
	require.Contains(t, joined, `env_key="OPENAI_API_KEY"`)
	require.Contains(t, joined, `wire_api="chat"`)
}
