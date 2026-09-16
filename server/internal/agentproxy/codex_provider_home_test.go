package agentproxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPrepareCodexProviderHome 生成官方式 CODEX_HOME：config.toml（Responses
// wire + bearer token）与 models.json（模型目录），密钥文件 0600。
func TestPrepareCodexProviderHome(t *testing.T) {
	dir, err := prepareCodexProviderHome(935, codexProviderEnv{
		BaseURL:       "https://open.bigmodel.cn/api/v1",
		APIKey:        "sk-test-key",
		Model:         "GLM-5.3-Flash[1m]",
		ContextWindow: 1000000,
	})
	require.NoError(t, err)
	require.Contains(t, dir, "codex-homes")
	require.Contains(t, dir, "ws-935")

	cfg, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	require.NoError(t, err)
	cfgStr := string(cfg)
	require.Contains(t, cfgStr, `model = "GLM-5.3-Flash[1m]"`)
	require.Contains(t, cfgStr, `model_provider = "niuniu-provider"`)
	require.Contains(t, cfgStr, `wire_api = "responses"`)
	require.Contains(t, cfgStr, `experimental_bearer_token = "sk-test-key"`)
	require.Contains(t, cfgStr, `base_url = "https://open.bigmodel.cn/api/v1"`)

	catalog, err := os.ReadFile(filepath.Join(dir, "models.json"))
	require.NoError(t, err)
	var parsed struct {
		Models []struct {
			Slug          string `json:"slug"`
			Priority      int    `json:"priority"`
			ContextWindow int64  `json:"context_window"`
		} `json:"models"`
	}
	require.NoError(t, json.Unmarshal(catalog, &parsed))
	require.Len(t, parsed.Models, 1)
	require.Equal(t, "GLM-5.3-Flash[1m]", parsed.Models[0].Slug)
	require.Equal(t, int64(1000000), parsed.Models[0].ContextWindow)
	// codex 严格解析：priority 等字段缺失会报 missing field（实测）。
	require.Equal(t, 0, parsed.Models[0].Priority)
	for _, field := range []string{"display_name", "default_reasoning_level", "shell_type", "supported_in_api", "input_modalities", "truncation_policy", "experimental_supported_tools"} {
		require.Contains(t, string(catalog), field)
	}

	// 再次生成覆盖写（跟随 provider 配置变化）。权限位 0600 仅在支持 Unix
	// 权限的平台上可断言（Windows 落盘为 0666）。
	info, err := os.Stat(filepath.Join(dir, "config.toml"))
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	}
	_ = strconv.Quote // 保持 strconv 引用（模板拼接使用）
}

// TestPrepareCodexProviderHome_IncompleteProvider：provider 未解析完整时返回
// 空目录（调用方走 codex 默认配置），不生成任何文件。
func TestPrepareCodexProviderHome_IncompleteProvider(t *testing.T) {
	dir, err := prepareCodexProviderHome(1, codexProviderEnv{BaseURL: "", APIKey: "k", Model: "m"})
	require.NoError(t, err)
	require.Empty(t, dir)
}
