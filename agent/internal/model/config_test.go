package model

import (
	"strings"
	"testing"
)

// clearModelEnv blanks every env var LoadConfig reads, so tests are
// hermetic even on machines (CI, dev boxes) that export ANTHROPIC_*/OPENAI_*
// for their own tooling.
func clearModelEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"NIUNIU_AGENT_PROVIDER",
		"ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL",
		"OPENAI_BASE_URL", "OPENAI_API_KEY", "OPENAI_MODEL",
	} {
		t.Setenv(k, "")
	}
}

func TestLoadConfigAnthropicDefaults(t *testing.T) {
	clearModelEnv(t)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "tok")
	t.Setenv("ANTHROPIC_MODEL", "glm-x")
	cfg, err := LoadConfig("", "")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Provider != ProviderAnthropic {
		t.Errorf("provider = %q", cfg.Provider)
	}
	if cfg.BaseURL != DefaultAnthropicBase {
		t.Errorf("base = %q, want default", cfg.BaseURL)
	}
	if cfg.Model != "glm-x" || cfg.AuthToken != "tok" {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestLoadConfigFlagOverridesEnv(t *testing.T) {
	clearModelEnv(t)
	t.Setenv("ANTHROPIC_BASE_URL", "https://gw.example.com/")
	t.Setenv("ANTHROPIC_API_KEY", "k")
	t.Setenv("ANTHROPIC_MODEL", "env-model")
	cfg, err := LoadConfig("", "flag-model")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Model != "flag-model" {
		t.Errorf("model = %q, want flag to win over env", cfg.Model)
	}
	// Trailing slash is trimmed so request URLs stay well-formed.
	if cfg.BaseURL != "https://gw.example.com" {
		t.Errorf("base = %q", cfg.BaseURL)
	}
}

func TestLoadConfigOpenAI(t *testing.T) {
	clearModelEnv(t)
	t.Setenv("NIUNIU_AGENT_PROVIDER", "openai")
	t.Setenv("OPENAI_API_KEY", "ok")
	t.Setenv("OPENAI_MODEL", "om")
	cfg, err := LoadConfig("", "")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Provider != ProviderOpenAI || cfg.BaseURL != DefaultOpenAIBase || cfg.Model != "om" {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestLoadConfigErrors(t *testing.T) {
	clearModelEnv(t)
	// No model configured.
	if _, err := LoadConfig("anthropic", ""); err == nil || !strings.Contains(err.Error(), "no model") {
		t.Errorf("err = %v, want missing-model error", err)
	}
	// Anthropic without credentials.
	t.Setenv("ANTHROPIC_MODEL", "m")
	if _, err := LoadConfig("anthropic", ""); err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Errorf("err = %v, want missing-credentials error", err)
	}
	// Unknown provider.
	t.Setenv("ANTHROPIC_API_KEY", "k")
	if _, err := LoadConfig("mistral", ""); err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("err = %v, want unknown-provider error", err)
	}
}
