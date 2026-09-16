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
	dir, err := CodexProviderHomeDir(workspaceID)
	if err != nil {
		return "", err
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

// CodexProviderHomeDir 返回工作空间的 provider CODEX_HOME 目录路径（不创建）。
func CodexProviderHomeDir(workspaceID int64) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("codex provider home dir: %w", err)
	}
	return filepath.Join(home, ".niuniu", "codex-homes", fmt.Sprintf("ws-%d", workspaceID)), nil
}

// RemoveCodexProviderHome 删除工作空间的 provider CODEX_HOME（含其中的中转
// 密钥文件）。工作空间删除时调用；目录不存在视为成功（RemoveAll 语义），
// 无法定位目录时返回错误。
func RemoveCodexProviderHome(workspaceID int64) error {
	dir, err := CodexProviderHomeDir(workspaceID)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("codex provider home: remove %s: %w", dir, err)
	}
	return nil
}

// codexModelsJSON 生成 codex 0.144+ 的模型目录文件。解析是严格模式（缺字段
// 即报 "missing field"，已实测 priority），字段集完整对照智谱官方样例。
func codexModelsJSON(model string, contextWindow int64) string {
	if contextWindow <= 0 {
		contextWindow = 131072
	}
	return `{
  "models": [
    {
      "slug": ` + strconv.Quote(model) + `,
      "display_name": ` + strconv.Quote(model) + `,
      "description": "niuniu provider model",
      "default_reasoning_level": "high",
      "supported_reasoning_levels": [
        { "effort": "low", "description": "Fast responses with lighter reasoning" },
        { "effort": "high", "description": "Extra high reasoning depth" },
        { "effort": "max", "description": "Maximum reasoning depth" }
      ],
      "shell_type": "shell_command",
      "visibility": "list",
      "supported_in_api": true,
      "priority": 0,
      "base_instructions": "",
      "supports_reasoning_summaries": true,
      "default_reasoning_summary": "none",
      "support_verbosity": false,
      "apply_patch_tool_type": "freeform",
      "truncation_policy": { "mode": "tokens", "limit": 10000 },
      "context_window": ` + strconv.FormatInt(contextWindow, 10) + `,
      "max_context_window": ` + strconv.FormatInt(contextWindow, 10) + `,
      "effective_context_window_percent": 95,
      "supports_parallel_tool_calls": true,
      "experimental_supported_tools": [],
      "input_modalities": ["text"]
    }
  ]
}
`
}
