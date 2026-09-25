package model

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Profile 是配置文件中的一个模型档案。凭据永不落入配置文件——只存
// 环境变量名（apiKeyEnv/authTokenEnv），运行时从 env 取实际值；
// 工作区配置文件会被提交进仓库，任何明文密钥都会被加载器拒绝。
type Profile struct {
	Provider     string `json:"provider"`
	BaseURL      string `json:"baseURL,omitempty"`
	APIKeyEnv    string `json:"apiKeyEnv,omitempty"`
	AuthTokenEnv string `json:"authTokenEnv,omitempty"`
	Model        string `json:"model,omitempty"`
	Thinking     string `json:"thinking,omitempty"` // off|low|medium|high|<tokens>
}

// ProfileFile 是 config.json 的文档结构。
type ProfileFile struct {
	DefaultProfile string             `json:"defaultProfile"`
	Profiles       map[string]Profile `json:"profiles"`
}

// plaintextSecretKeys 是配置文件中禁止出现的明文密钥字段名。
var plaintextSecretKeys = []string{"apikey", "authtoken", "api_key", "auth_token", "token", "secret"}

// ParseProfileFile 解析 config.json；出现明文密钥字段直接报错。
func ParseProfileFile(data []byte) (*ProfileFile, error) {
	var probe map[string]any
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if rawProfiles, ok := probe["profiles"].(map[string]any); ok {
		for pname, pv := range rawProfiles {
			fields, ok := pv.(map[string]any)
			if !ok {
				continue
			}
			for key := range fields {
				norm := strings.ToLower(strings.ReplaceAll(key, "-", ""))
				for _, bad := range plaintextSecretKeys {
					if norm == bad {
						return nil, fmt.Errorf(
							"SECURITY: profile %q contains plaintext secret field %q — "+
								"store credentials in environment variables and reference them "+
								"via apiKeyEnv/authTokenEnv (config files may be committed)", pname, key)
					}
				}
			}
		}
	}
	var pf ProfileFile
	if err := json.Unmarshal(data, &pf); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &pf, nil
}

// Unmarshal re-parses the file document (helper for callers holding raw
// bytes from LoadChain).
func (pf *ProfileFile) Unmarshal(data []byte) error {
	parsed, err := ParseProfileFile(data)
	if err != nil {
		return err
	}
	*pf = *parsed
	return nil
}

// LoadProfileFile reads and parses a config.json.
func LoadProfileFile(path string) (*ProfileFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseProfileFile(data)
}

// FindProfileFile returns the most specific existing config path:
// project (<cwd>/.niuniu-agent/config.json) before global
// (~/.niuniu-agent/config.json). ok=false when neither exists.
func FindProfileFile(cwd, home string) (string, bool) {
	candidates := []string{
		filepath.Join(cwd, ".niuniu-agent", "config.json"),
	}
	if home != "" {
		candidates = append(candidates, filepath.Join(home, ".niuniu-agent", "config.json"))
	} else if h, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(h, ".niuniu-agent", "config.json"))
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
	}
	return "", false
}

// Flags carries explicit CLI overrides (empty string = not set). APIKey/
// AuthToken are literal values from flags (rare; env refs are preferred).
type Flags struct {
	Profile   string
	Provider  string
	Model     string
	BaseURL   string
	APIKey    string
	AuthToken string
}

// Resolve synthesizes the final Config by merging layers, most specific
// wins per field:
//
//	CLI flags > process env (ANTHROPIC_*/OPENAI_*/NIUNIU_AGENT_PROFILE,
//	workspace-injected) > project config > global config > built-in defaults.
//
// profileData may be nil (no config file) — the legacy env-only path from
// LoadConfig then applies, keeping P0-P6 setups working unchanged.
//
// Credential resolution for a profile: authTokenEnv/apiKeyEnv name the env
// var to read; when unset, the standard ANTHROPIC_*/OPENAI_* vars are
// consulted. Plaintext secrets never appear in config files (enforced by
// ParseProfileFile).
func Resolve(profileData []byte, globalData []byte, flags Flags, cwd string) (Config, error) {
	// —— profile 选择：flag > env > project.default > global.default ——
	profileName := firstNonEmpty(flags.Profile, os.Getenv("NIUNIU_AGENT_PROFILE"))
	// Explicit selection (-profile / NIUNIU_AGENT_PROFILE) is a strong
	// intent: the profile's own fields must take effect wholesale, or a
	// same-name standard env (e.g. a workspace-injected ANTHROPIC_* pointing
	// at another gateway) silently hijacks the chosen profile. Implicit
	// defaultProfile keeps the softer "env overrides config" semantics.
	explicitProfile := profileName != ""
	var project, global *ProfileFile
	if len(profileData) > 0 {
		pf, err := ParseProfileFile(profileData)
		if err != nil {
			return Config{}, err
		}
		project = pf
	}
	if len(globalData) > 0 {
		pf, err := ParseProfileFile(globalData)
		if err != nil {
			return Config{}, fmt.Errorf("global config: %w", err)
		}
		global = pf
	}
	if profileName == "" {
		if project != nil && project.DefaultProfile != "" {
			profileName = project.DefaultProfile
		} else if global != nil && global.DefaultProfile != "" {
			profileName = global.DefaultProfile
		}
	}

	// 无任何 profile 定义 → legacy env-only 路径（P0 语义，向后兼容）。
	hasProfiles := (project != nil && len(project.Profiles) > 0) ||
		(global != nil && len(global.Profiles) > 0)
	if profileName == "" && !hasProfiles {
		return LoadConfig(flags.Provider, flags.Model)
	}
	if profileName == "" {
		profileName = "default"
	}

	// profile 定义合成：project 覆盖 global 的同名 profile。
	var prof Profile
	found := false
	for _, pf := range []*ProfileFile{project, global} {
		if pf == nil {
			continue
		}
		if p, ok := pf.Profiles[profileName]; ok {
			prof = p
			found = true
			break
		}
	}
	if !found && profileName != "default" {
		available := make([]string, 0, 8)
		if project != nil {
			for n := range project.Profiles {
				available = append(available, n)
			}
		}
		if global != nil {
			for n := range global.Profiles {
				available = append(available, n)
			}
		}
		return Config{}, fmt.Errorf("unknown profile %q (available: %s)",
			profileName, strings.Join(available, ", "))
	}

	// —— 字段合成：flag > env > profile > 内置默认 ——
	provider := firstNonEmpty(flags.Provider, os.Getenv("NIUNIU_AGENT_PROVIDER"), prof.Provider, ProviderAnthropic)

	var modelEnv, baseEnv, credPrimary, credSecondary string
	switch provider {
	case ProviderOpenAI:
		modelEnv, baseEnv = "OPENAI_MODEL", "OPENAI_BASE_URL"
		credPrimary, credSecondary = "OPENAI_API_KEY", ""
	default:
		modelEnv, baseEnv = "ANTHROPIC_MODEL", "ANTHROPIC_BASE_URL"
		credPrimary, credSecondary = "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"
	}

	// 凭据：flag 明文（少见）> profile 指定的 env 名 > 该 provider 标准 env。
	authToken := firstNonEmpty(flags.AuthToken, derefEnv(prof.AuthTokenEnv), os.Getenv(credPrimary))
	apiKey := firstNonEmpty(flags.APIKey, derefEnv(prof.APIKeyEnv), os.Getenv(credSecondary))

	// 字段覆盖序：flag 最高。显式 profile 时 profile 字段压过标准 env
	// （env 退为兜底）；隐式 defaultProfile 时标准 env 反压 profile
	// 字段（env 作为临时覆盖手段，P7a 语义）。
	baseURL, modelField := os.Getenv(baseEnv), os.Getenv(modelEnv)
	if explicitProfile {
		baseURL = firstNonEmpty(prof.BaseURL, baseURL)
		modelField = firstNonEmpty(prof.Model, modelField)
	} else {
		baseURL = firstNonEmpty(baseURL, prof.BaseURL)
		modelField = firstNonEmpty(modelField, prof.Model)
	}

	cfg := Config{
		Provider:  provider,
		BaseURL:   firstNonEmpty(flags.BaseURL, baseURL, defaultBaseFor(provider)),
		APIKey:    apiKey,
		AuthToken: authToken,
		Model:     firstNonEmpty(flags.Model, modelField),
		Thinking:  parseThinking(firstNonEmpty(os.Getenv("NIUNIU_AGENT_THINKING"), prof.Thinking)),
		Stream:    ParseStreamFlag(os.Getenv("NIUNIU_AGENT_STREAM")),
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if i := strings.IndexByte(cfg.Model, '['); i > 0 {
		cfg.Model = cfg.Model[:i]
	}
	if cfg.Model == "" {
		return cfg, fmt.Errorf("no model configured: set model in profile %q or the provider's *_MODEL env var", profileName)
	}
	if provider == ProviderAnthropic && cfg.APIKey == "" && cfg.AuthToken == "" {
		return cfg, fmt.Errorf("no anthropic credentials for profile %q: set %s (or the standard env)",
			profileName, orDefault(prof.AuthTokenEnv, prof.APIKeyEnv, credPrimary))
	}
	if provider == ProviderOpenAI && cfg.APIKey == "" {
		return cfg, fmt.Errorf("no openai credentials for profile %q: set %s (or OPENAI_API_KEY)",
			profileName, orDefault(prof.APIKeyEnv, "OPENAI_API_KEY"))
	}
	return cfg, nil
}

func defaultBaseFor(provider string) string {
	if provider == ProviderOpenAI {
		return DefaultOpenAIBase
	}
	return DefaultAnthropicBase
}

func orDefault(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// derefEnv reads the env var named by s (empty name → empty value).
func derefEnv(name string) string {
	if name == "" {
		return ""
	}
	return os.Getenv(name)
}
