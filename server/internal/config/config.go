package config

import (
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/viper"
)

// isLoopbackHost reports whether the configured bind host only reaches the local
// machine (so no other host on the network can connect).
func isLoopbackHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "", "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// EnforceLoopbackWhenUnauthenticated forces the bind host back to loopback when
// authentication is disabled (personal edition) and the configured host would
// otherwise expose the server on a non-loopback interface. An unauthenticated
// niuniu bound to 0.0.0.0 hands any host on the network full API access —
// including the workspace PTY, which is a remote shell. Set
// NIUNIU_ALLOW_INSECURE_BIND=1 to opt out on a trusted isolated network.
// Returns the original host and true when it overrode the binding, so the
// caller can log the change.
func EnforceLoopbackWhenUnauthenticated(cfg *Config) (original string, overridden bool) {
	if cfg.Auth.Enabled || isLoopbackHost(cfg.Server.Host) {
		return cfg.Server.Host, false
	}
	if os.Getenv("NIUNIU_ALLOW_INSECURE_BIND") != "" {
		return cfg.Server.Host, false
	}
	original = cfg.Server.Host
	cfg.Server.Host = "127.0.0.1"
	return original, true
}

type Config struct {
	Server            ServerConfig            `mapstructure:"server"`
	Storage           StorageConfig           `mapstructure:"storage"`
	Workspace         WorkspaceConfig         `mapstructure:"workspace"`
	Agent             AgentConfig             `mapstructure:"agent"`
	Log               LogConfig               `mapstructure:"log"`
	AI                AIConfig                `mapstructure:"ai"`
	Auth              AuthConfig              `mapstructure:"auth"`
	AgentRegistry     AgentRegistryConfig     `mapstructure:"agent_registry"`
	Harness           HarnessConfig           `mapstructure:"harness"`
	Editor            EditorConfig            `mapstructure:"editor"`
	Upgrade           UpgradeConfig           `mapstructure:"upgrade"`
	ImageOptimization ImageOptimizationConfig `mapstructure:"image_optimization"`
	Orchestration     OrchestrationConfig     `mapstructure:"orchestration"`
	Telemetry         TelemetryConfig         `mapstructure:"telemetry"`
	DataDir           string                  // computed at load time, not from config file (~/.niuniu)
}

// TelemetryConfig is the master opt-out switch for the anonymous
// personal-edition "app opened" telemetry (Epic #329). Enabled defaults to true
// (opt-out): the reporter only runs on personal / self-hosted single-user
// instances (Auth.Enabled=false), re-reads Enabled on every tick, and skips the
// report when it is false, so flipping it off stops uploads at the next
// heartbeat without a restart. The flag is persisted to config.yaml on first
// start (see Load, mirroring server.id) and rewritten by the /api/config setter
// via Save.
type TelemetryConfig struct {
	Enabled bool `mapstructure:"enabled"`
}

// OrchestrationConfig holds the cost guardrails that bound the AI-native board
// orchestrator (spec 2026-06-05 section 16, phase 6). They MUST be in place
// before the orchestrator fan-out lands. A value <= 0 disables that particular
// guardrail. See docs/superpowers/specs/2026-06-06-orchestration-cost-guardrails-design.md.
//
// The default MaxConcurrentWorkspaces (5) intentionally tracks the CLAUDE.md
// "SQLite single-writer; switch to Postgres above ~3 concurrent agents" rule:
// run more concurrent orchestration workspaces than that and you should move
// storage.driver to postgres.
type OrchestrationConfig struct {
	// MaxBatchIssues caps how many issues one batch_create_issues call may create.
	MaxBatchIssues int `mapstructure:"max_batch_issues"`
	// MaxConcurrentWorkspaces caps concurrently-active workspaces per owner
	// (non-archived and status<>'completed'); over the cap, start_workspace
	// queues instead of rejects.
	MaxConcurrentWorkspaces int `mapstructure:"max_concurrent_workspaces"`
	// ChainCostBudgetUSD caps the cumulative cost of one orchestration tree
	// (an epic plus its child workspaces).
	ChainCostBudgetUSD float64 `mapstructure:"chain_cost_budget_usd"`
	// ChainCostWarnRatio is the fraction of the budget at which start_workspace
	// still admits but flags the orchestrator to ask_user before fanning out more.
	ChainCostWarnRatio float64 `mapstructure:"chain_cost_warn_ratio"`
}

type ImageOptimizationConfig struct {
	Enabled           bool  `mapstructure:"enabled"`
	TriggerLongEdgePx int   `mapstructure:"trigger_long_edge_px"`
	TriggerSizeBytes  int64 `mapstructure:"trigger_size_bytes"`
	TargetLongEdgePx  int   `mapstructure:"target_long_edge_px"`
	TargetMaxBytes    int64 `mapstructure:"target_max_bytes"`
	MinQuality        int   `mapstructure:"min_quality"`
	MinLongEdgePx     int   `mapstructure:"min_long_edge_px"`
}

type HarnessConfig struct {
	Enabled bool `mapstructure:"enabled"`
}

type EditorConfig struct {
	VSCodeMode      string `mapstructure:"vscode_mode"`       // "local" or "remote"
	VSCodeRemoteURL string `mapstructure:"vscode_remote_url"` // e.g. "http://localhost:8080"
}

type AIConfig struct {
	AnthropicAPIKey string `mapstructure:"anthropic_api_key"`
}

type AuthConfig struct {
	Enabled       bool         `mapstructure:"enabled"`
	TokenExpiry   string       `mapstructure:"token_expiry"`
	RefreshExpiry string       `mapstructure:"refresh_expiry"`
	Users         []UserConfig `mapstructure:"users"`
	SingleUser    UserConfig   `mapstructure:"single_user"` // used when Enabled=false
}

type UpgradeConfig struct {
	DefaultOwner string `mapstructure:"default_owner"` // "", "org:<name>", or "user:<username>"
}

// ResolvedUpgradeTarget returns ("user"|"org", value) after applying defaults.
func (c *Config) ResolvedUpgradeTarget() (string, string) {
	if c.Upgrade.DefaultOwner != "" {
		if strings.HasPrefix(c.Upgrade.DefaultOwner, "user:") {
			return "user", strings.TrimPrefix(c.Upgrade.DefaultOwner, "user:")
		}
		if strings.HasPrefix(c.Upgrade.DefaultOwner, "org:") {
			return "org", strings.TrimPrefix(c.Upgrade.DefaultOwner, "org:")
		}
	}
	if !c.Auth.Enabled {
		username := c.Auth.SingleUser.Username
		if username == "" {
			username = "local"
		}
		return "user", username
	}
	return "org", "Default"
}

type UserConfig struct {
	Username    string `mapstructure:"username"`
	Password    string `mapstructure:"password"`
	DisplayName string `mapstructure:"display_name"`
	Role        string `mapstructure:"role"`
}

type ServerConfig struct {
	Port int    `mapstructure:"port"`
	Host string `mapstructure:"host"`
	ID   string `mapstructure:"id"`
}

type StorageConfig struct {
	Driver   string          `mapstructure:"driver"`
	SQLite   SQLiteConfig    `mapstructure:"sqlite"`
	Postgres PostgresConfig  `mapstructure:"postgres"`
	KB       KBStorageConfig `mapstructure:"kb"`
}

// KBStorageConfig holds knowledge-base storage policy.
type KBStorageConfig struct {
	// AllowLocalSources gates reading arbitrary server-local paths as a KB
	// source: a source_kind=local pointing outside the owner's datasets dir,
	// file:// URLs, and local/file git clones. Left as a pointer so an absent key
	// (nil) can be distinguished from an explicit true/false: when unset it is
	// derived from the edition (see Config.KBAllowLocalSources). The single-tenant
	// personal edition (auth disabled) wants it true — reading a local corpus is
	// the intended use; the multi-tenant hosted edition (auth enabled) wants it
	// false so a tenant can't turn a KB source into an arbitrary-file-read
	// primitive (/etc, other tenants' datasets). Env override:
	// NIUNIU_STORAGE_KB_ALLOW_LOCAL_SOURCES.
	AllowLocalSources *bool `mapstructure:"allow_local_sources"`
}

type SQLiteConfig struct {
	Path string `mapstructure:"path"`
	// MaxOpenConns caps the SQLite connection pool. With WAL + busy_timeout +
	// BEGIN IMMEDIATE (see store.Open), >1 lets reads run concurrently with the
	// single writer instead of serialising on one connection. Default 8; set to
	// 1 to fall back to the old strict single-connection behaviour.
	MaxOpenConns int `mapstructure:"max_open_conns"`
	// MmapSize is the mmap_size PRAGMA in bytes (default 128MB). Memory-mapped
	// I/O speeds sequential reads significantly. Set to 0 to disable on
	// low-memory machines.
	MmapSize int64 `mapstructure:"mmap_size"`
	// AgentMessageRetentionDays controls how many days to keep agent_messages
	// for archived workspaces before pruning. Default 90. 0 disables pruning.
	AgentMessageRetentionDays int `mapstructure:"agent_message_retention_days"`
}

type PostgresConfig struct {
	DSN string `mapstructure:"dsn"`
}

type WorkspaceConfig struct {
	BaseDir        string            `mapstructure:"base_dir"`
	DefaultEnvVars map[string]string `mapstructure:"default_env_vars"`
}

type AgentConfig struct {
	// NOTE: a former `default` field (global default engine) was removed — the
	// engine a workspace runs is decided per-workspace (workspaces.cli_type) and
	// per-project (projects.default_cli_type), so a global default had no
	// consumer. Per-CLI command/args below remain (they locate/override each
	// engine's binary).
	ClaudeCode ClaudeCodeConfig `mapstructure:"claude_code"`
	CodexCli   CodexCliConfig   `mapstructure:"codex_cli"`
	QwenCli    QwenCliConfig    `mapstructure:"qwen_cli"`
}

type LogConfig struct {
	Level         string `mapstructure:"level"`
	Output        string `mapstructure:"output"`
	FileDir       string `mapstructure:"file_dir"`
	RetentionDays int    `mapstructure:"retention_days"`
}

type ClaudeCodeConfig struct {
	Command string   `mapstructure:"command"`
	Args    []string `mapstructure:"args"`
}

// CodexCliConfig configures the OpenAI Codex CLI binary used by codex-type
// workspaces. Mirrors ClaudeCodeConfig. Default Command "codex" is set in
// the Viper defaults block.
type CodexCliConfig struct {
	Command string   `mapstructure:"command"`
	Args    []string `mapstructure:"args"`
}

// QwenCliConfig configures the Qwen Code CLI binary used by qwen-type
// workspaces (niuniu's third agent engine). Mirrors CodexCliConfig. Default
// Command "qwen" is set in the Viper defaults block.
type QwenCliConfig struct {
	Command string   `mapstructure:"command"`
	Args    []string `mapstructure:"args"`
}

type AgentRegistryConfig struct {
	CustomAgentsDir string          `mapstructure:"custom_agents_dir"`
	Registries      []RegistryEntry `mapstructure:"registries"`
}

type RegistryEntry struct {
	Name     string `mapstructure:"name"`
	Type     string `mapstructure:"type"`
	URL      string `mapstructure:"url"`
	Enabled  bool   `mapstructure:"enabled"`
	CacheTTL int    `mapstructure:"cache_ttl"`
}

// MCPGenerateOptions controls which tools the unified MCP server registers.
type MCPGenerateOptions struct {
	ProjectID    int64
	WorkspaceID  int64
	InboxDir     string
	AgentName    string
	SessionToken string // raw MCP session token — written into env.NIUNIU_MCP_TOKEN in .mcp.json
	// CodexSandboxMode / CodexApprovalPolicy are only used by codex config.toml
	// (top-level keys). Empty = generator skips the line (codex uses its
	// built-in defaults). Spec: 2026-05-22-codex-full-support-design.md B1.
	CodexSandboxMode    string
	CodexApprovalPolicy string
	// ExtraMCPServers are inline MCP server entries (name -> ".mcp.json" server
	// object) written verbatim into the generated config. Scene projection uses
	// this to carry user-authored MCP JSON snippets that are NOT in the Claude
	// registry. An inline entry overrides a registry-resolved extra of the same
	// name. For codex config.toml only the command/args/env shape is supported.
	ExtraMCPServers map[string]map[string]any
	// DisableToolGroups lists niuniu built-in MCP tool groups to hide from the
	// agent (passed through as the niuniu-mcp `--disable-tool-groups` flag).
	// Empty = register every group. Set by scene projection from the active
	// scene's disable_tool_groups.
	DisableToolGroups []string
	// EnableToolGroups lists opt-in niuniu built-in MCP tool groups that are OFF
	// by default and must be turned on explicitly (niuniu-mcp
	// `--enable-tool-groups`). Used for privacy-sensitive tools (e.g.
	// browser-history). Set by scene projection from the active scene's
	// enable_tool_groups. Empty = no opt-in group registered.
	EnableToolGroups []string
}

func Load() (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	niuniuDir := filepath.Join(home, ".niuniu")
	os.MkdirAll(niuniuDir, 0755)

	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(niuniuDir)

	// Enable env var overrides: NIUNIU_SERVER_PORT, NIUNIU_AUTH_ENABLED, etc.
	viper.SetEnvPrefix("NIUNIU")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	// storage.kb.allow_local_sources intentionally has NO viper default: its
	// zero value depends on the edition (derived from auth.enabled in
	// KBAllowLocalSources), and a default would make the *bool always non-nil,
	// defeating that. Bind the env explicitly so an override is still picked up by
	// Unmarshal despite the missing default.
	_ = viper.BindEnv("storage.kb.allow_local_sources")

	// Defaults
	viper.SetDefault("server.port", 3000)
	viper.SetDefault("server.host", "127.0.0.1")
	viper.SetDefault("storage.driver", "sqlite")
	viper.SetDefault("storage.sqlite.path", filepath.Join(niuniuDir, "niuniu.db"))
	viper.SetDefault("storage.sqlite.max_open_conns", 8)
	viper.SetDefault("storage.sqlite.mmap_size", int64(134217728)) // 128MB
	viper.SetDefault("storage.sqlite.agent_message_retention_days", 90)
	viper.SetDefault("workspace.base_dir", filepath.Join(niuniuDir, "workspaces"))
	viper.SetDefault("agent.claude_code.command", "claude")
	viper.SetDefault("agent.claude_code.args", []string{})
	viper.SetDefault("agent.codex_cli.command", "codex")
	viper.SetDefault("agent.codex_cli.args", []string{})
	viper.SetDefault("agent.qwen_cli.command", "qwen")
	viper.SetDefault("agent.qwen_cli.args", []string{})
	viper.SetDefault("log.level", "debug")
	viper.SetDefault("log.output", "terminal")
	viper.SetDefault("log.file_dir", "")
	viper.SetDefault("log.retention_days", 30)
	viper.SetDefault("agent_registry.custom_agents_dir", filepath.Join(niuniuDir, "agents"))
	viper.SetDefault("auth.enabled", true)
	viper.SetDefault("auth.token_expiry", "15m")
	viper.SetDefault("auth.refresh_expiry", "168h")
	viper.SetDefault("auth.single_user.username", "local")
	viper.SetDefault("auth.users", []map[string]interface{}{
		{
			"username":     "niuniu",
			"password":     "niuniu123",
			"display_name": "Niuniu",
			"role":         "admin",
		},
	})
	viper.SetDefault("harness.enabled", true)
	viper.SetDefault("editor.vscode_mode", "local")
	viper.SetDefault("editor.vscode_remote_url", "")
	viper.SetDefault("image_optimization.enabled", true)
	viper.SetDefault("image_optimization.trigger_long_edge_px", 1280)
	viper.SetDefault("image_optimization.trigger_size_bytes", 102400)
	viper.SetDefault("image_optimization.target_long_edge_px", 1280)
	viper.SetDefault("image_optimization.target_max_bytes", 81920)
	viper.SetDefault("image_optimization.min_quality", 40)
	viper.SetDefault("image_optimization.min_long_edge_px", 512)
	viper.SetDefault("orchestration.max_batch_issues", 20)
	viper.SetDefault("orchestration.max_concurrent_workspaces", 5)
	viper.SetDefault("orchestration.chain_cost_budget_usd", 10.0)
	viper.SetDefault("orchestration.chain_cost_warn_ratio", 0.8)
	// Anonymous personal-edition telemetry defaults ON (opt-out). Only takes
	// effect on Auth.Enabled=false instances; see TelemetryConfig.
	viper.SetDefault("telemetry.enabled", true)

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, err
		}
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, err
	}
	cfg.DataDir = niuniuDir
	needWrite := false
	if cfg.Server.ID == "" {
		cfg.Server.ID = uuid.New().String()
		viper.Set("server.id", cfg.Server.ID)
		needWrite = true
	}
	// Persist the telemetry opt-out flag on first start, mirroring server.id
	// above. The default is true, so a bool zero-value can't distinguish "never
	// set" from "user turned it off" — instead rely on InConfig, which is true
	// only when the key is physically present in config.yaml. This writes the
	// key exactly once (fresh install or first boot after upgrade) and never
	// clobbers a value the user later set via the /api/config setter.
	if !viper.InConfig("telemetry.enabled") {
		viper.Set("telemetry.enabled", cfg.Telemetry.Enabled)
		needWrite = true
	}
	if needWrite {
		_ = viper.WriteConfigAs(filepath.Join(niuniuDir, "config.yaml"))
	}
	return &cfg, nil
}

// KBAllowLocalSources reports whether reading arbitrary server-local paths as a
// KB source is permitted. An explicit storage.kb.allow_local_sources wins;
// otherwise it defaults by edition — permitted on the single-tenant personal
// edition (auth disabled), refused on the multi-tenant hosted edition (auth
// enabled). See KBStorageConfig.AllowLocalSources.
func (c *Config) KBAllowLocalSources() bool {
	if c.Storage.KB.AllowLocalSources != nil {
		return *c.Storage.KB.AllowLocalSources
	}
	return !c.Auth.Enabled
}

func GetAuthSecret() string {
	if s := os.Getenv("NIUNIU_AUTH_SECRET"); s != "" {
		return s
	}
	home, _ := os.UserHomeDir()
	secretPath := filepath.Join(home, ".niuniu", "auth_secret")
	data, err := os.ReadFile(secretPath)
	if err == nil && len(data) > 0 {
		return strings.TrimSpace(string(data))
	}
	secret := uuid.New().String() + uuid.New().String()
	if err := os.WriteFile(secretPath, []byte(secret), 0600); err != nil {
		slog.Error("failed to persist auth secret", "error", err, "path", secretPath)
	}
	return secret
}

func Save(cfg *Config) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	viper.Set("server.port", cfg.Server.Port)
	viper.Set("server.host", cfg.Server.Host)
	viper.Set("workspace.base_dir", cfg.Workspace.BaseDir)
	viper.Set("agent.claude_code.command", cfg.Agent.ClaudeCode.Command)
	viper.Set("agent.claude_code.args", cfg.Agent.ClaudeCode.Args)
	viper.Set("agent.codex_cli.command", cfg.Agent.CodexCli.Command)
	viper.Set("agent.codex_cli.args", cfg.Agent.CodexCli.Args)
	viper.Set("agent.qwen_cli.command", cfg.Agent.QwenCli.Command)
	viper.Set("agent.qwen_cli.args", cfg.Agent.QwenCli.Args)
	viper.Set("editor.vscode_mode", cfg.Editor.VSCodeMode)
	viper.Set("editor.vscode_remote_url", cfg.Editor.VSCodeRemoteURL)
	viper.Set("telemetry.enabled", cfg.Telemetry.Enabled)

	return viper.WriteConfigAs(filepath.Join(home, ".niuniu", "config.yaml"))
}
