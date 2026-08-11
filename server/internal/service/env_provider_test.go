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
		Protocol:      "anthropic",
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

// providerEnvOpenAI is the same provider configured for an OpenAI-compatible
// endpoint (different protocol + base_url), as real platforms differ.
func providerEnvOpenAI() store.EnvProvider {
	p := providerEnv()
	p.Protocol = "openai"
	p.BaseUrl = "https://api.deepseek.com/v1"
	return p
}

func TestExpand_Claude(t *testing.T) {
	s := &EnvProviderService{}
	env := s.Expand(context.Background(), providerEnv(), CLILauncherClaude, pAccounts, false)
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
	// An OpenAI-protocol provider (e.g. api.deepseek.com/v1) produces the
	// OpenAI-family env read by Codex. The same provider configured for the
	// /anthropic endpoint would instead produce ANTHROPIC_* (see TestExpand_Claude).
	env := s.Expand(context.Background(), providerEnvOpenAI(), CLILauncherCodex, pAccounts, false)
	want := map[string]string{
		"OPENAI_BASE_URL": "https://api.deepseek.com/v1",
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
	env := s.Expand(context.Background(), providerEnvOpenAI(), CLILauncherQwen, pAccounts, false)
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
	env := s.Expand(context.Background(), providerEnv(), CLILauncherClaude, nil, false)
	if _, ok := env["ANTHROPIC_AUTH_TOKEN"]; ok {
		t.Errorf("unresolved account should omit credential key, got %v", env)
	}
	if env["ANTHROPIC_BASE_URL"] != "https://api.deepseek.com/anthropic" {
		t.Errorf("non-credential fields still expand: %v", env)
	}
}

func TestExpand_Empty(t *testing.T) {
	s := &EnvProviderService{}
	if env := s.Expand(context.Background(), store.EnvProvider{}, CLILauncherClaude, nil, false); len(env) != 0 {
		t.Errorf("empty provider should expand to empty map, got %v", env)
	}
}

func TestExpand_PreserveRef_KeepsAccountReference(t *testing.T) {
	s := &EnvProviderService{}
	// preserveRef=true emits the ${ACCOUNT:name} reference unchanged so an
	// imported preset stays runtime-live (sceneenv substitutes at spawn).
	env := s.Expand(context.Background(), providerEnv(), CLILauncherClaude, pAccounts, true)
	if env["ANTHROPIC_AUTH_TOKEN"] != "${ACCOUNT:DeepSeek}" {
		t.Errorf("preserveRef should keep the reference, got %q", env["ANTHROPIC_AUTH_TOKEN"])
	}
	// Non-credential fields still expand normally.
	if env["ANTHROPIC_BASE_URL"] != "https://api.deepseek.com/anthropic" {
		t.Errorf("base_url should still expand, got %q", env["ANTHROPIC_BASE_URL"])
	}
}