package model

import (
	"os"
	"testing"
)

func TestTierModel(t *testing.T) {
	t.Setenv(EnvModelHigh, "glm-5.3-airx")
	t.Setenv(EnvModelFast, "glm-5.3-flash")

	if got := TierModel(TierHigh); got != "glm-5.3-airx" {
		t.Errorf("TierModel(high) = %q", got)
	}
	if got := TierModel(TierFast); got != "glm-5.3-flash" {
		t.Errorf("TierModel(fast) = %q", got)
	}
	if got := TierModel(""); got != "" {
		t.Errorf("TierModel(empty) = %q, want empty (base)", got)
	}
	if got := TierModel("bogus"); got != "" {
		t.Errorf("TierModel(unknown) = %q, want empty", got)
	}
}

func TestTierModelUnset(t *testing.T) {
	os.Unsetenv(EnvModelHigh)
	if got := TierModel(TierHigh); got != "" {
		t.Errorf("TierModel(high) with env unset = %q, want empty (base fallback)", got)
	}
}

func TestWithModel(t *testing.T) {
	cfg := Config{Provider: ProviderAnthropic, Model: "glm-5.3", Thinking: ThinkingConfig{Effort: "medium"}}
	tier := cfg.WithModel("glm-5.3-airx")
	if tier.Model != "glm-5.3-airx" {
		t.Errorf("tier.Model = %q", tier.Model)
	}
	if tier.Provider != cfg.Provider || tier.Thinking.Effort != "medium" {
		t.Errorf("WithModel must inherit provider/thinking, got %+v", tier)
	}
	if cfg.Model != "glm-5.3" {
		t.Errorf("WithModel mutated the receiver: %q", cfg.Model)
	}
}
