// provider 对 codex 的接入：codex-cli ≥0.13x 只支持 Responses API 协议
// （wire_api="chat" 已移除，见 upstream discussion #7782），且第三方模型的
// 接入方式是文件配置——CODEX_HOME 下的 config.toml（model_provider +
// experimental_bearer_token）与 models.json（模型元数据目录，官方样例见
// 智谱/DeepSeek 的 codex 对接文档）。环境变量 OPENAI_BASE_URL/OPENAI_API_KEY
// 只影响 API-key 模式的默认端点，且换不了 wire_api，因此 provider 绑定必须
// 落成文件。
//
// 每个绑定了 provider 的工作空间生成独立的 CODEX_HOME（互不污染，也不动用户
// 的全局 ~/.codex）：~/.niuniu/codex-homes/ws-<id>/{config.toml,models.json}，
// codex 启动时以 CODEX_HOME 指向它。

package agentproxy

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// codexProviderEnv 是从工作空间生效环境变量里提取的 provider 接入要素。
type codexProviderEnv struct {
	BaseURL       string // 中转的 codex 端点（Responses API）
	APIKey        string // 已解析账号引用的中转密钥
	Model         string // 中转侧模型名（作为 codex model 与 models.json slug）
	ContextWindow int64  // 0 = 未知（写入目录时取保守默认）
}

// prepareCodexProviderHome 生成/刷新工作空间的 provider CODEX_HOME，返回目录
// 路径。BaseURL/APIKey/Model 任一为空表示 provider 未解析或不完整，返回 ""
// 由调用方走 codex 默认配置。密钥以 0600 权限落在用户目录下（与 config.yaml
// 同级保护），每次 spawn 覆盖写以跟随 provider 配置变化。
func prepareCodexProviderHome(workspaceID int64, e codexProviderEnv) (string, error) {
	if e.BaseURL == "" || e.APIKey == "" || e.Model == "" {
		return "", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("codex provider home: %w", err)
	}
	dir := filepath.Join(home, ".niuniu", "codex-homes", fmt.Sprintf("ws-%d", workspaceID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("codex provider home: %w", err)
	}
	catalog := filepath.ToSlash(filepath.Join(dir, "models.json"))
	if err := os.WriteFile(catalog, []byte(codexModelsJSON(e.Model, e.ContextWindow)), 0o600); err != nil {
		return "", fmt.Errorf("codex provider home: write models.json: %w", err)
	}
	cfg := "model = " + strconv.Quote(e.Model) + "\n" +
		"model_provider = \"niuniu-provider\"\n" +
		"preferred_auth_method = \"apikey\"\n" +
		"forced_login_method = \"api\"\n" +
		"model_catalog_json = " + strconv.Quote(catalog) + "\n" +
		"\n[model_providers.niuniu-provider]\n" +
		"name = \"niuniu-provider\"\n" +
		"base_url = " + strconv.Quote(e.BaseURL) + "\n" +
		"wire_api = \"responses\"\n" +
		"experimental_bearer_token = " + strconv.Quote(e.APIKey) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfg), 0o600); err != nil {
		return "", fmt.Errorf("codex provider home: write config.toml: %w", err)
	}
	return dir, nil
}

// codexModelsJSON 生成 codex 0.144+ 的模型目录文件（字段集对照智谱/DeepSeek
// 官方 models.json 样例裁剪：缺省字段由 codex 取默认值）。
func codexModelsJSON(model string, contextWindow int64) string {
	if contextWindow <= 0 {
		contextWindow = 131072
	}
	return `{
  "models": [
    {
      "slug": ` + strconv.Quote(model) + `,
      "display_name": ` + strconv.Quote(model) + `,
      "default_reasoning_level": "high",
      "supported_reasoning_levels": [
        { "effort": "low", "description": "Low" },
        { "effort": "medium", "description": "Medium" },
        { "effort": "high", "description": "High" }
      ],
      "shell_type": "shell_command",
      "visibility": "list",
      "supported_in_api": true,
      "input_modalities": ["text"],
      "context_window": ` + strconv.FormatInt(contextWindow, 10) + `,
      "max_context_window": ` + strconv.FormatInt(contextWindow, 10) + `,
      "effective_context_window_percent": 95,
      "supports_parallel_tool_calls": true,
      "apply_patch_tool_type": "freeform",
      "truncation_policy": { "mode": "tokens", "limit": 10000 }
    }
  ]
}
`
}
