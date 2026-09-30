package model

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// Supported provider families.
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
)

// Default API roots.
const (
	DefaultAnthropicBase = "https://api.anthropic.com"
	DefaultOpenAIBase    = "https://api.openai.com/v1"
)

// Config selects and authenticates a provider endpoint.
type Config struct {
	// Provider is ProviderAnthropic or ProviderOpenAI.
	Provider string
	// BaseURL is the API root without trailing slash.
	BaseURL string
	// APIKey authenticates via the provider's key header (Anthropic:
	// x-api-key, OpenAI: Bearer).
	APIKey string
	// AuthToken authenticates via Authorization: Bearer — the common scheme
	// for Anthropic-compatible gateways (GLM and friends).
	AuthToken string
	// Model is the model name sent to the API.
	Model string
	// Thinking is the reasoning budget/effort parsed from
	// NIUNIU_AGENT_THINKING (off | low | medium | high | <tokens>).
	Thinking ThinkingConfig
	// Stream enables SSE streaming (NIUNIU_AGENT_STREAM, default on).
	Stream bool
	// Retry bounds 429/5xx/network retries (zero value → DefaultRetryPolicy;
	// set Max explicitly to disable).
	Retry RetryPolicy
}

// LoadConfig builds a Config from environment variables, applying flag
// overrides. Precedence for the model: -model flag > provider env var.
// Provider: -provider flag > NIUNIU_AGENT_PROVIDER > anthropic.
//
// Anthropic family: ANTHROPIC_BASE_URL (default api.anthropic.com),
// ANTHROPIC_API_KEY (x-api-key) or ANTHROPIC_AUTH_TOKEN (Bearer), ANTHROPIC_MODEL.
// OpenAI family: OPENAI_BASE_URL (default api.openai.com/v1), OPENAI_API_KEY,
// OPENAI_MODEL.
func LoadConfig(providerFlag, modelFlag string) (Config, error) {
	provider := firstNonEmpty(providerFlag, os.Getenv("NIUNIU_AGENT_PROVIDER"), ProviderAnthropic)
	cfg := Config{Provider: provider}
	switch provider {
	case ProviderAnthropic:
		cfg.BaseURL = firstNonEmpty(os.Getenv("ANTHROPIC_BASE_URL"), DefaultAnthropicBase)
		cfg.APIKey = os.Getenv("ANTHROPIC_API_KEY")
		cfg.AuthToken = os.Getenv("ANTHROPIC_AUTH_TOKEN")
		cfg.Model = firstNonEmpty(modelFlag, os.Getenv("ANTHROPIC_MODEL"))
	case ProviderOpenAI:
		cfg.BaseURL = firstNonEmpty(os.Getenv("OPENAI_BASE_URL"), DefaultOpenAIBase)
		cfg.APIKey = os.Getenv("OPENAI_API_KEY")
		cfg.Model = firstNonEmpty(modelFlag, os.Getenv("OPENAI_MODEL"))
	default:
		return Config{}, fmt.Errorf("unknown provider %q (want %q or %q)", provider, ProviderAnthropic, ProviderOpenAI)
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	// Thinking budget: explicit NIUNIU_AGENT_THINKING wins; otherwise known
	// MoE/reasoning model families get their recommended tier automatically
	// (registry.DefaultThinkingTier) so deep reasoning is on by default for
	// the models that can do it, not something each workspace must remember.
	if v := os.Getenv("NIUNIU_AGENT_THINKING"); v != "" {
		cfg.Thinking = parseThinking(v)
	} else {
		cfg.Thinking = parseThinking(DefaultThinkingTier(cfg.Model))
	}
	cfg.Stream = ParseStreamFlag(os.Getenv("NIUNIU_AGENT_STREAM"))
	// "[1m]"-style context-tier suffixes are an upstream harness decoration
	// (the workspace model picker reuses Claude-style tiered names); the wire
	// API rejects them, so normalize here at the protocol boundary.
	if i := strings.IndexByte(cfg.Model, '['); i > 0 {
		cfg.Model = cfg.Model[:i]
	}
	switch {
	case cfg.Model == "":
		return Config{}, errors.New("no model configured: pass -model or set the provider's *_MODEL env var")
	case provider == ProviderAnthropic && cfg.APIKey == "" && cfg.AuthToken == "":
		return Config{}, errors.New("no anthropic credentials: set ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN")
	case provider == ProviderOpenAI && cfg.APIKey == "":
		return Config{}, errors.New("no openai credentials: set OPENAI_API_KEY")
	}
	return cfg, nil
}

// parseThinking maps NIUNIU_AGENT_THINKING onto a ThinkingConfig:
// "off"/"" → zero (provider default), low|medium|high → effort with a
// matching token budget (both protocol families get something usable),
// numeric → an explicit Anthropic-style budget.
// parseStreamFlag: streaming is ON unless explicitly disabled
// (NIUNIU_AGENT_STREAM=0|off|false).
func ParseStreamFlag(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "off", "false", "no":
		return false
	}
	return true
}

func parseThinking(v string) ThinkingConfig {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "off", "none", "0":
		return ThinkingConfig{}
	case "low":
		return ThinkingConfig{BudgetTokens: 2048, Effort: "low"}
	case "medium":
		return ThinkingConfig{BudgetTokens: 8192, Effort: "medium"}
	case "high":
		return ThinkingConfig{BudgetTokens: 16384, Effort: "high"}
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
		return ThinkingConfig{BudgetTokens: n}
	}
	return ThinkingConfig{}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
