package service

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
)

var pAccounts = []store.EnvAccount{
	{Name: "DeepSeek", ApiKey: "sk-real"},
}

func providerEnv() store.EnvProvider {
	return store.EnvProvider{
		Name:          "DeepSeek",
		BaseUrl:       "https://api.deepseek.com/anthropic",
		ApiKey:        "${ACCOUNT:DeepSeek}",
		Model:         "deepseek-v4",
		HaikuModel:    "deepseek-v4-flash",
		SonnetModel:   "deepseek-v4-pro",
		OpusModel:     "deepseek-v4-pro",
		SubagentModel: "deepseek-v4-flash",
		ExtraEnv:      `{"CLAUDE_CODE_EFFORT_LEVEL":"max"}`,
	}
}

func TestExpand_Claude(t *testing.T) {
	s := &EnvProviderService{}
	env := s.Expand(context.Background(), providerEnv(), CLILauncherClaude, pAccounts)
	want := map[string]string{
		"ANTHROPIC_BASE_URL":             "https://api.deepseek.com/anthropic",
		"ANTHROPIC_AUTH_TOKEN":           "sk-real",
		"ANTHROPIC_MODEL":                "deepseek-v4",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "deepseek-v4-flash",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "deepseek-v4-pro",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   "deepseek-v4-pro",
		"CLAUDE_CODE_SUBAGENT_MODEL":     "deepseek-v4-flash",
		"CLAUDE_CODE_EFFORT_LEVEL":       "max",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("claude env %s = %q, want %q", k, env[k], v)
		}
	}
}

func TestExpand_Codex(t *testing.T) {
	s := &EnvProviderService{}
	env := s.Expand(context.Background(), providerEnv(), CLILauncherCodex, pAccounts)
	want := map[string]string{
		"OPENAI_BASE_URL": "https://api.deepseek.com/anthropic",
		"OPENAI_API_KEY":  "sk-real",
		"OPENAI_MODEL":    "deepseek-v4",
		"NIUNIU_MODEL":    "deepseek-v4",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("codex env %s = %q, want %q", k, env[k], v)
		}
	}
	// Codex must not carry Anthropic tiered-model keys.
	if _, ok := env["ANTHROPIC_DEFAULT_OPUS_MODEL"]; ok {
		t.Error("codex env should not include ANTHROPIC_DEFAULT_OPUS_MODEL")
	}
}

func TestExpand_Qwen(t *testing.T) {
	s := &EnvProviderService{}
	env := s.Expand(context.Background(), providerEnv(), CLILauncherQwen, pAccounts)
	if env["OPENAI_API_KEY"] != "sk-real" || env["OPENAI_MODEL"] != "deepseek-v4" {
		t.Errorf("qwen env wrong: %v", env)
	}
	if _, ok := env["NIUNIU_MODEL"]; ok {
		t.Error("qwen env should not include NIUNIU_MODEL")
	}
}

func TestExpand_AccountRefUnresolved_OmitsKey(t *testing.T) {
	s := &EnvProviderService{}
	// No matching account → the ANTHROPIC_AUTH_TOKEN / OPENAI_API_KEY key is
	// omitted entirely rather than emitted as "${ACCOUNT:Missing}".
	env := s.Expand(context.Background(), providerEnv(), CLILauncherClaude, nil)
	if _, ok := env["ANTHROPIC_AUTH_TOKEN"]; ok {
		t.Errorf("unresolved account should omit credential key, got %v", env)
	}
	if env["ANTHROPIC_BASE_URL"] != "https://api.deepseek.com/anthropic" {
		t.Errorf("non-credential fields still expand: %v", env)
	}
}

func TestExpand_Empty(t *testing.T) {
	s := &EnvProviderService{}
	if env := s.Expand(context.Background(), store.EnvProvider{}, CLILauncherClaude, nil); len(env) != 0 {
		t.Errorf("empty provider should expand to empty map, got %v", env)
	}
}