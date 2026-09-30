package model

import (
	"testing"
)

// LoadConfig must auto-apply the recommended thinking tier for known
// reasoning model families when NIUNIU_AGENT_THINKING is unset — deep
// reasoning ON by default for models that can do it, not per-workspace
// tribal knowledge. An explicit env value always wins.
func TestLoadConfig_AutoThinkingTier(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "https://example.com")
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("NIUNIU_AGENT_THINKING", "")

	t.Run("moe family gets auto tier", func(t *testing.T) {
		t.Setenv("ANTHROPIC_MODEL", "glm-5.3")
		cfg, err := LoadConfig("", "")
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.Thinking.Effort != "medium" {
			t.Errorf("glm-5.3 thinking effort = %q, want medium (auto)", cfg.Thinking.Effort)
		}
	})

	t.Run("explicit env wins over auto tier", func(t *testing.T) {
		t.Setenv("ANTHROPIC_MODEL", "glm-5.3")
		t.Setenv("NIUNIU_AGENT_THINKING", "high")
		cfg, err := LoadConfig("", "")
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.Thinking.Effort != "high" {
			t.Errorf("thinking effort = %q, want high (explicit)", cfg.Thinking.Effort)
		}
	})

	t.Run("unknown family stays provider default", func(t *testing.T) {
		t.Setenv("ANTHROPIC_MODEL", "mystery-model-x")
		cfg, err := LoadConfig("", "")
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.Thinking.Effort != "" {
			t.Errorf("thinking effort = %q, want empty (no auto tier for unknown family)", cfg.Thinking.Effort)
		}
	})

	t.Run("tier env composes with tier model override", func(t *testing.T) {
		t.Setenv("ANTHROPIC_MODEL", "glm-5.3")
		t.Setenv(EnvModelHigh, "glm-5.3-airx")
		cfg, err := LoadConfig("", TierModel(TierHigh))
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.Model != "glm-5.3-airx" {
			t.Errorf("tier model = %q, want glm-5.3-airx", cfg.Model)
		}
		if cfg.Thinking.Effort != "medium" {
			t.Errorf("tier model auto thinking = %q, want medium (airx still glm family)", cfg.Thinking.Effort)
		}
	})
}
