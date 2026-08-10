package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// newOrchTestViper mirrors the orchestration defaults + env binding wired into
// Load(), so the tests exercise the same key names and mapstructure tags without
// touching the global viper / the user's real ~/.niuniu/config.yaml.
func newOrchTestViper() *viper.Viper {
	v := viper.New()
	v.SetEnvPrefix("NIUNIU")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	v.SetDefault("orchestration.max_batch_issues", 20)
	v.SetDefault("orchestration.max_concurrent_workspaces", 5)
	v.SetDefault("orchestration.chain_cost_budget_usd", 10.0)
	v.SetDefault("orchestration.chain_cost_warn_ratio", 0.8)
	return v
}

func TestOrchestrationDefaults(t *testing.T) {
	var cfg Config
	if err := newOrchTestViper().Unmarshal(&cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	o := cfg.Orchestration
	if o.MaxBatchIssues != 20 {
		t.Errorf("MaxBatchIssues=%d, want 20", o.MaxBatchIssues)
	}
	if o.MaxConcurrentWorkspaces != 5 {
		t.Errorf("MaxConcurrentWorkspaces=%d, want 5", o.MaxConcurrentWorkspaces)
	}
	if o.ChainCostBudgetUSD != 10.0 {
		t.Errorf("ChainCostBudgetUSD=%v, want 10.0", o.ChainCostBudgetUSD)
	}
	if o.ChainCostWarnRatio != 0.8 {
		t.Errorf("ChainCostWarnRatio=%v, want 0.8", o.ChainCostWarnRatio)
	}
}

func TestOrchestrationEnvOverride(t *testing.T) {
	t.Setenv("NIUNIU_ORCHESTRATION_MAX_CONCURRENT_WORKSPACES", "2")
	t.Setenv("NIUNIU_ORCHESTRATION_CHAIN_COST_BUDGET_USD", "3.5")
	var cfg Config
	if err := newOrchTestViper().Unmarshal(&cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Orchestration.MaxConcurrentWorkspaces != 2 {
		t.Errorf("MaxConcurrentWorkspaces=%d, want 2 (env override)", cfg.Orchestration.MaxConcurrentWorkspaces)
	}
	if cfg.Orchestration.ChainCostBudgetUSD != 3.5 {
		t.Errorf("ChainCostBudgetUSD=%v, want 3.5 (env override)", cfg.Orchestration.ChainCostBudgetUSD)
	}
}

func TestTelemetry_DefaultEnabled(t *testing.T) {
	v := viper.New()
	v.SetDefault("telemetry.enabled", true)

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !cfg.Telemetry.Enabled {
		t.Errorf("Telemetry.Enabled default should be true (opt-out)")
	}
}

func TestTelemetry_FileValueOverridesDefault(t *testing.T) {
	// A persisted false must survive: the opt-out flag is read off config.yaml,
	// not re-defaulted to true on every load.
	v := viper.New()
	v.SetDefault("telemetry.enabled", true)
	v.Set("telemetry.enabled", false) // simulates the value read from config.yaml

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Telemetry.Enabled {
		t.Errorf("Telemetry.Enabled = true, want false (persisted opt-out must win)")
	}
}

func TestImageOptimization_Defaults(t *testing.T) {
	v := viper.New()
	v.SetDefault("image_optimization.enabled", true)
	v.SetDefault("image_optimization.trigger_long_edge_px", 1280)
	v.SetDefault("image_optimization.trigger_size_bytes", 102400)
	v.SetDefault("image_optimization.target_long_edge_px", 1280)
	v.SetDefault("image_optimization.target_max_bytes", 81920)
	v.SetDefault("image_optimization.min_quality", 40)
	v.SetDefault("image_optimization.min_long_edge_px", 512)

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !cfg.ImageOptimization.Enabled {
		t.Errorf("Enabled default should be true")
	}
	if cfg.ImageOptimization.TriggerLongEdgePx != 1280 {
		t.Errorf("TriggerLongEdgePx=%d, want 1280", cfg.ImageOptimization.TriggerLongEdgePx)
	}
	if cfg.ImageOptimization.TriggerSizeBytes != 102400 {
		t.Errorf("TriggerSizeBytes=%d, want 102400", cfg.ImageOptimization.TriggerSizeBytes)
	}
	if cfg.ImageOptimization.TargetLongEdgePx != 1280 {
		t.Errorf("TargetLongEdgePx=%d, want 1280", cfg.ImageOptimization.TargetLongEdgePx)
	}
	if cfg.ImageOptimization.TargetMaxBytes != 81920 {
		t.Errorf("TargetMaxBytes=%d, want 81920", cfg.ImageOptimization.TargetMaxBytes)
	}
	if cfg.ImageOptimization.MinQuality != 40 {
		t.Errorf("MinQuality=%d, want 40", cfg.ImageOptimization.MinQuality)
	}
	if cfg.ImageOptimization.MinLongEdgePx != 512 {
		t.Errorf("MinLongEdgePx=%d, want 512", cfg.ImageOptimization.MinLongEdgePx)
	}
}

func TestConfigUpgradeDefaultOwnerAutoDetect(t *testing.T) {
	tests := []struct {
		name        string
		authEnabled bool
		explicit    string
		wantType    string
		wantValue   string
	}{
		{"auth-off default", false, "", "user", "local"},
		{"auth-on default", true, "", "org", "Default"},
		{"explicit org", true, "org:Acme", "org", "Acme"},
		{"explicit user", false, "user:alice", "user", "alice"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Auth:    AuthConfig{Enabled: tc.authEnabled, SingleUser: UserConfig{Username: "local"}},
				Upgrade: UpgradeConfig{DefaultOwner: tc.explicit},
			}
			gotType, gotValue := cfg.ResolvedUpgradeTarget()
			if gotType != tc.wantType || gotValue != tc.wantValue {
				t.Fatalf("got %s:%s want %s:%s", gotType, gotValue, tc.wantType, tc.wantValue)
			}
		})
	}
}

// boolPtr is a tiny helper for the *bool KBStorageConfig field.
func boolPtr(b bool) *bool { return &b }

func TestKBAllowLocalSources_EditionDefault(t *testing.T) {
	// Unset (nil) => derived from edition: personal (auth off) permits, hosted
	// (auth on) refuses.
	cases := []struct {
		name        string
		authEnabled bool
		want        bool
	}{
		{"personal_auth_off", false, true},
		{"hosted_auth_on", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{}
			cfg.Auth.Enabled = tc.authEnabled
			if cfg.Storage.KB.AllowLocalSources != nil {
				t.Fatalf("precondition: AllowLocalSources should default nil")
			}
			if got := cfg.KBAllowLocalSources(); got != tc.want {
				t.Errorf("KBAllowLocalSources()=%v, want %v (auth.enabled=%v)", got, tc.want, tc.authEnabled)
			}
		})
	}
}

func TestKBAllowLocalSources_ExplicitWins(t *testing.T) {
	// An explicit value overrides the edition default in both directions.
	cfg := Config{}
	cfg.Auth.Enabled = true // hosted default would be false
	cfg.Storage.KB.AllowLocalSources = boolPtr(true)
	if !cfg.KBAllowLocalSources() {
		t.Errorf("explicit true must win over hosted default false")
	}

	cfg2 := Config{}
	cfg2.Auth.Enabled = false // personal default would be true
	cfg2.Storage.KB.AllowLocalSources = boolPtr(false)
	if cfg2.KBAllowLocalSources() {
		t.Errorf("explicit false must win over personal default true")
	}
}

func TestKBAllowLocalSources_EnvOverride(t *testing.T) {
	// The bound env var must reach the *bool through Unmarshal despite there
	// being no viper default for the key.
	t.Setenv("NIUNIU_STORAGE_KB_ALLOW_LOCAL_SOURCES", "true")
	v := viper.New()
	v.SetEnvPrefix("NIUNIU")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	_ = v.BindEnv("storage.kb.allow_local_sources")

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Storage.KB.AllowLocalSources == nil {
		t.Fatal("env override not picked up: AllowLocalSources is nil")
	}
	if !*cfg.Storage.KB.AllowLocalSources {
		t.Errorf("AllowLocalSources=%v, want true (env override)", *cfg.Storage.KB.AllowLocalSources)
	}
}
