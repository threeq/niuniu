package model

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeProfile(t *testing.T, dir, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const glmProfileJSON = `{
	"defaultProfile": "glm",
	"profiles": {
		"glm": {
			"provider": "anthropic",
			"baseURL": "https://open.bigmodel.cn/api/anthropic",
			"authTokenEnv": "GLM_TOKEN",
			"model": "GLM-5.3-Flash",
			"thinking": "off"
		},
		"local": {
			"provider": "openai",
			"baseURL": "http://localhost:11434/v1",
			"apiKeyEnv": "LOCAL_KEY",
			"model": "qwen2.5-coder"
		}
	}
}`

func clearProfileEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"NIUNIU_AGENT_PROVIDER", "NIUNIU_AGENT_PROFILE", "NIUNIU_AGENT_STREAM",
		"NIUNIU_AGENT_THINKING",
		"ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL",
		"OPENAI_BASE_URL", "OPENAI_API_KEY", "OPENAI_MODEL",
		"GLM_TOKEN", "LOCAL_KEY",
	} {
		t.Setenv(k, "")
	}
}

func TestParseProfileFile(t *testing.T) {
	pf, err := ParseProfileFile([]byte(glmProfileJSON))
	if err != nil {
		t.Fatalf("ParseProfileFile: %v", err)
	}
	if pf.DefaultProfile != "glm" || len(pf.Profiles) != 2 {
		t.Fatalf("pf = %+v", pf)
	}
	glm := pf.Profiles["glm"]
	if glm.Provider != "anthropic" || glm.BaseURL != "https://open.bigmodel.cn/api/anthropic" ||
		glm.AuthTokenEnv != "GLM_TOKEN" || glm.Model != "GLM-5.3-Flash" {
		t.Fatalf("glm profile = %+v", glm)
	}
}

// 安全闸：配置文件里出现明文密钥字段直接拒绝加载。
func TestParseProfileRejectsPlaintextSecrets(t *testing.T) {
	for _, body := range []string{
		`{"profiles":{"x":{"provider":"anthropic","apiKey":"sk-plaintext"}}}`,
		`{"profiles":{"x":{"provider":"anthropic","authToken":"tok-plaintext"}}}`,
		`{"profiles":{"x":{"provider":"openai","api_key":"sk-plain"}}}`,
	} {
		if _, err := ParseProfileFile([]byte(body)); err == nil || !strings.Contains(err.Error(), "plaintext") {
			t.Errorf("body %s: err = %v, want plaintext-secret rejection", body, err)
		}
	}
}

func TestFindProfileFileProjectOverGlobal(t *testing.T) {
	root := t.TempDir()
	globalDir := filepath.Join(root, "home", ".niuniu-agent")
	projectDir := filepath.Join(root, "proj", ".niuniu-agent")
	writeProfile(t, globalDir, `{"defaultProfile":"g","profiles":{"g":{"provider":"anthropic","model":"g-model"}}}`)
	writeProfile(t, projectDir, `{"defaultProfile":"p","profiles":{"p":{"provider":"openai","model":"p-model"}}}`)

	path, ok := FindProfileFile(filepath.Join(root, "proj"), filepath.Join(root, "home"))
	if !ok || !strings.Contains(path, filepath.Join("proj", ".niuniu-agent")) {
		t.Fatalf("find = %q, %v; want project path", path, ok)
	}
	// 无项目级时回退全局。
	path2, ok2 := FindProfileFile(filepath.Join(root, "other"), filepath.Join(root, "home"))
	if !ok2 || !strings.Contains(path2, filepath.Join("home", ".niuniu-agent")) {
		t.Fatalf("find fallback = %q, %v; want global path", path2, ok2)
	}
	// 两处皆无 → not ok。
	if _, ok3 := FindProfileFile(filepath.Join(root, "other2"), filepath.Join(root, "home2")); ok3 {
		t.Error("want not-ok when no config exists")
	}
}

func TestResolvePicksProfileAndEnvCredentials(t *testing.T) {
	clearProfileEnv(t)
	t.Setenv("GLM_TOKEN", "tok-from-env")
	cfg, err := Resolve([]byte(glmProfileJSON), nil, Flags{}, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Provider != ProviderAnthropic || cfg.Model != "GLM-5.3-Flash" ||
		cfg.BaseURL != "https://open.bigmodel.cn/api/anthropic" || cfg.AuthToken != "tok-from-env" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if cfg.APIKey != "" {
		t.Errorf("apikey should be empty when authTokenEnv resolves")
	}
	// 凭据 env 未设 → 报错（不得静默空凭据）。
	t.Setenv("GLM_TOKEN", "")
	if _, err := Resolve([]byte(glmProfileJSON), nil, Flags{}, ""); err == nil {
		t.Fatal("want error when credential env is unset")
	}
}

func TestResolveEnvOverridesProfileFields(t *testing.T) {
	clearProfileEnv(t)
	t.Setenv("GLM_TOKEN", "tok")
	t.Setenv("ANTHROPIC_MODEL", "env-model-wins")
	t.Setenv("ANTHROPIC_BASE_URL", "https://env.example/api")
	cfg, err := Resolve([]byte(glmProfileJSON), nil, Flags{}, "")
	if err != nil {
		t.Fatal(err)
	}
	// workspace/用户 env 高于项目 config 的同名字段。
	if cfg.Model != "env-model-wins" || cfg.BaseURL != "https://env.example/api" {
		t.Fatalf("env must override profile fields: %+v", cfg)
	}
}

func TestResolveFlagsHighest(t *testing.T) {
	clearProfileEnv(t)
	t.Setenv("GLM_TOKEN", "tok")
	cfg, err := Resolve([]byte(glmProfileJSON), nil, Flags{
		Profile:   "local",
		Model:     "flag-model",
		BaseURL:   "https://flag.example/v1",
		APIKey:    "flag-key",
		AuthToken: "flag-tok",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != ProviderOpenAI || cfg.Model != "flag-model" ||
		cfg.BaseURL != "https://flag.example/v1" || cfg.APIKey != "flag-key" {
		t.Fatalf("cfg = %+v, want flag overrides on local profile", cfg)
	}
}

func TestResolveProfileFlagSelects(t *testing.T) {
	clearProfileEnv(t)
	t.Setenv("GLM_TOKEN", "tok")
	t.Setenv("LOCAL_KEY", "lk")
	cfg, err := Resolve([]byte(glmProfileJSON), nil, Flags{Profile: "local"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != ProviderOpenAI || cfg.Model != "qwen2.5-coder" {
		t.Fatalf("cfg = %+v, want local profile", cfg)
	}
	// 未知 profile 名报错并列出可用。
	_, err = Resolve([]byte(glmProfileJSON), nil, Flags{Profile: "nope"}, "")
	if err == nil || !strings.Contains(err.Error(), "glm") {
		t.Fatalf("err = %v, want unknown-profile listing", err)
	}
}

func TestResolveThinkingFromProfile(t *testing.T) {
	clearProfileEnv(t)
	t.Setenv("GLM_TOKEN", "tok")
	cfg, err := Resolve([]byte(glmProfileJSON), nil, Flags{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Thinking.BudgetTokens != 0 || cfg.Thinking.Effort != "" {
		t.Fatalf("thinking = %+v, want off (profile thinking=off)", cfg.Thinking)
	}
	// env 覆盖 profile。
	t.Setenv("NIUNIU_AGENT_THINKING", "high")
	cfg2, _ := Resolve([]byte(glmProfileJSON), nil, Flags{}, "")
	if cfg2.Thinking.Effort != "high" {
		t.Fatalf("env thinking = %+v", cfg2.Thinking)
	}
}

func TestResolveFallsBackToLegacyWithoutConfig(t *testing.T) {
	clearProfileEnv(t)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "tok")
	t.Setenv("ANTHROPIC_MODEL", "glm-x")
	cfg, err := Resolve(nil, nil, Flags{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "glm-x" || cfg.Provider != ProviderAnthropic {
		t.Fatalf("cfg = %+v, want legacy env path", cfg)
	}
}

func TestProfileDefaultsToAnthropicBaseWhenUnset(t *testing.T) {
	clearProfileEnv(t)
	body := `{"defaultProfile":"d","profiles":{"d":{"provider":"anthropic","authTokenEnv":"T","model":"m"}}}`
	t.Setenv("T", "tok")
	cfg, err := Resolve([]byte(body), nil, Flags{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != DefaultAnthropicBase {
		t.Fatalf("base = %q, want anthropic default", cfg.BaseURL)
	}
}

// 显式选择 profile（-profile / NIUNIU_AGENT_PROFILE）时，profile 的
// baseURL/model/凭据整体生效——否则同名标准 env（如 workspace 注入的
// ANTHROPIC_*）会把显式选中的 profile 抢回别的网关，profile 形同虚设。
// flag 仍最高；隐式 defaultProfile 时 env 依旧优先（临时覆盖语义）。
func TestResolveExplicitProfileBeatsStandardEnv(t *testing.T) {
	clearProfileEnv(t)
	t.Setenv("GLM_TOKEN", "tok")
	// 标准 env 全部指向「别的」端点与模型。
	t.Setenv("ANTHROPIC_MODEL", "env-model")
	t.Setenv("ANTHROPIC_BASE_URL", "https://env.example/api")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "env-tok")
	t.Setenv("ANTHROPIC_API_KEY", "env-key")

	cfg, err := Resolve([]byte(glmProfileJSON), nil, Flags{Profile: "glm"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "https://open.bigmodel.cn/api/anthropic" || cfg.Model != "GLM-5.3-Flash" {
		t.Fatalf("explicit profile must beat standard env: baseURL=%q model=%q", cfg.BaseURL, cfg.Model)
	}
	if cfg.AuthToken != "tok" {
		t.Fatalf("credential chain broken: token=%q", cfg.AuthToken)
	}
	// 未声明的 APIKeyEnv 允许标准 env 兜底混入——运行时 Bearer 优先
	//（anthropic_test 已钉住 header 二选一），无实际危害。

	// flag 仍最高。
	cfg2, err := Resolve([]byte(glmProfileJSON), nil, Flags{Profile: "glm", Model: "flag-model"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.Model != "flag-model" {
		t.Fatalf("flag must stay highest: %q", cfg2.Model)
	}

	// 隐式 defaultProfile：env 依旧优先（既有语义不回归）。
	cfg3, err := Resolve([]byte(glmProfileJSON), nil, Flags{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg3.Model != "env-model" || cfg3.BaseURL != "https://env.example/api" {
		t.Fatalf("implicit defaultProfile keeps env override: %+v", cfg3)
	}
}
