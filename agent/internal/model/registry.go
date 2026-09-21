package model

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Provider registry: name → factory(Config) (Model). anthropic/openai are
// built in; a new vendor family (gemini, …) implements the same Model
// interface and registers itself — profiles reference it by the registered
// name via the profile's "provider" field.
var (
	registryMu sync.RWMutex
	registry   = map[string]func(Config) (Model, error){}
)

// RegisterProvider adds (or replaces) a provider family factory.
func RegisterProvider(name string, factory func(Config) (Model, error)) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[name] = factory
}

// ProviderNames returns the registered provider families, sorted.
func ProviderNames() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// NewForProvider builds a Model from the registry. Unknown names error with
// the available list.
func NewForProvider(name string, cfg Config) (Model, error) {
	registryMu.RLock()
	factory, ok := registry[name]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown provider %q (registered: %v)", name, ProviderNames())
	}
	return factory(cfg)
}

func init() {
	RegisterProvider(ProviderAnthropic, func(cfg Config) (Model, error) {
		if cfg.APIKey == "" && cfg.AuthToken == "" {
			return nil, fmt.Errorf("no anthropic credentials")
		}
		return NewAnthropic(cfg), nil
	})
	RegisterProvider(ProviderOpenAI, func(cfg Config) (Model, error) {
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("no openai credentials")
		}
		return NewOpenAI(cfg), nil
	})
}

// MoE/reasoning-capable model families and their default thinking tier
// (profiles may override per profile). Keyword-matched case-insensitively
// against the model name.
var moeThinkingDefaults = []struct {
	match string
	tier  string // low|medium|high
}{
	{"glm", "medium"},
	{"kimi", "medium"},
	{"deepseek-r", "high"},
	{"qwen3", "low"},
}

// DefaultThinkingTier returns the recommended thinking tier for a model
// name ("" = unknown family, keep provider default).
func DefaultThinkingTier(modelName string) string {
	low := strings.ToLower(modelName)
	for _, m := range moeThinkingDefaults {
		if strings.Contains(low, m.match) {
			return m.tier
		}
	}
	return ""
}

// NewForProviderOrLegacy builds the Model for a fully-resolved Config. It
// exists so hosts with pre-P7 Config construction keep one entry point.
func NewForProviderOrLegacy(cfg Config) (Model, error) {
	return NewForProvider(cfg.Provider, cfg)
}
