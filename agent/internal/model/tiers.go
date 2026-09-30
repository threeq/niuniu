package model

import (
	"fmt"
	"os"
	"strings"
)

// Model capacity tiers, Claude-CLI-style: "high" ≈ opus-class (deep
// reasoning: planning, review of subtle failures), "fast" ≈ haiku-class
// (cheap mechanical work: bulk exploration), and the implicit default ≈
// sonnet-class (the workspace's configured base model). Tiers are NAMES, not
// models — each resolves through the tier env vars so any provider family
// can serve any tier.
const (
	TierHigh = "high"
	TierFast = "fast"
)

// Tier env vars. Unset/empty tier env → the base model serves that tier
// (graceful degradation: a workspace that configures only NIUNIU_MODEL gets
// correct, if undifferentiated, behavior).
const (
	EnvModelHigh = "NIUNIU_AGENT_MODEL_HIGH"
	EnvModelFast = "NIUNIU_AGENT_MODEL_FAST"
)

// TierModel returns the override model name configured for a tier ("" = use
// the base model). Unknown tiers return "".
func TierModel(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case TierHigh:
		return strings.TrimSpace(os.Getenv(EnvModelHigh))
	case TierFast:
		return strings.TrimSpace(os.Getenv(EnvModelFast))
	default:
		return ""
	}
}

// WithModel returns a copy of cfg with a different model name. Everything
// else (provider, credentials, thinking, streaming) is inherited — a tier is
// a model swap inside the same provider contract.
func (c Config) WithModel(modelName string) Config {
	c.Model = modelName
	return c
}

// DescribeTier renders a human-readable tier→model mapping for diagnostics.
func DescribeTier(baseModel, tier string) string {
	if m := TierModel(tier); m != "" {
		return fmt.Sprintf("%s=%s", tier, m)
	}
	return fmt.Sprintf("%s=%s (base)", tier, baseModel)
}
