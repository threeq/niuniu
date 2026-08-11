package adapter

// Type identifies which CLI a session uses. Mirrors workspaces.cli_type.
type Type string

const (
	TypeClaude Type = "claude"
	TypeCodex  Type = "codex"
	// TypeQwen is niuniu's third agent engine: Qwen Code (Alibaba, Apache-2.0),
	// the first that runs no foreign closed-source CLI. See
	// docs/superpowers/specs/2026-06-29-domestic-general-agent-engine-selection.md.
	TypeQwen Type = "qwen"
	// TypeOmp is niuniu's general-purpose agent backend: oh-my-pi (omp), driven
	// over `omp --mode rpc` NDJSON-over-stdio. Unlike the other three it is not
	// a parse-the-stdout adapter — the session layer routes TypeOmp workspaces
	// to the agentbackend.omp Backend (see agentproxy.Send). This Type value
	// keeps the workspace cli_type enum uniform and gives the bridge a stable
	// dispatch key.
	TypeOmp Type = "omp"
	// TypeGoose is niuniu's general-purpose agent backend: Block's Goose, driven
	// over the Agent Client Protocol (ACP) on stdio (`goose acp`). Like omp it is
	// not a parse-the-stdout adapter — the session layer routes TypeGoose
	// workspaces to the agentbackend.goose Backend (see agentproxy.Send). Goose
	// is MCP-first (70+ extensions) and plays MCP client to niuniu-mcp.
	TypeGoose Type = "goose"
)

// Adapter encapsulates the CLI-specific concerns that vary between Claude and
// Codex (and future CLIs): how to parse one line of stdout into a ParsedEvent,
// and which Type label to stamp on emitted events.
//
// Adapters are stateless: each call gets all the context it needs as
// arguments. Per-session state (in-flight tool calls, parse cursors, etc.)
// lives on the caller (WorkspaceSession in the agentproxy package).
type Adapter interface {
	// Type returns "claude" or "codex" so the session layer can stamp
	// MessageEvent.CliType correctly.
	Type() Type

	// ProcessMode tells the shared session runtime how this CLI consumes turns.
	// The runtime owns DB/SSE/process orchestration; the adapter only declares
	// the driver shape.
	ProcessMode() ProcessMode

	// DisplayName returns the CLI name used in emitted metadata. command may be
	// empty; implementations fall back to their default binary name.
	DisplayName(command string) string

	// ParseLine parses one NDJSON line. Returns zero or more events. For
	// Claude the protocol guarantees one event per line so the slice will
	// have at most one entry; for Codex a single JSON-RPC notification can
	// fan out into multiple semantic events (e.g. plan_update -> TodoWrite
	// start + delta + stop + tool_result).
	ParseLine(line string) ([]ParsedEvent, error)

	// BuildSpawn returns the executable and argv for a CLI process. It is pure:
	// callers resolve workspace rows, worktrees, config defaults, and env
	// overrides before invoking it.
	BuildSpawn(opts SpawnOptions) (string, []string)

	// InjectEnv applies CLI-specific env behavior on top of the host env and
	// workspace env vars. It filters NIUNIU_* control keys, honors user-set
	// account env vars, and injects CLAUDE_CONFIG_DIR / CODEX_HOME when a
	// managed account config dir is resolved.
	InjectEnv(base []string, opts EnvOptions) []string

	// PermissionArgs returns CLI flags for permission handling. Claude maps this
	// to --permission-mode / --permission-prompt-tool / allowlists; Codex uses
	// config.toml approval_policy instead, so its implementation returns nil.
	PermissionArgs(opts PermissionOptions) []string
}

type ProcessMode string

const (
	ProcessLongRunning ProcessMode = "long_running"
	ProcessOneShot     ProcessMode = "one_shot"
)

// For returns the right Adapter for the given CLI type. Defaults to Claude
// for unknown / empty inputs (matches the legacy "no cli_type" semantics).
func For(t Type) Adapter {
	switch t {
	case TypeCodex:
		return CodexAdapter{}
	case TypeQwen:
		return QwenAdapter{}
	case TypeOmp:
		return OmpAdapter{}
	case TypeGoose:
		return GooseAdapter{}
	}
	return ClaudeAdapter{}
}

// SpawnOptions captures the CLI-specific command line surface while keeping
// store/service dependencies outside the adapter package.
type SpawnOptions struct {
	Command      string
	ExtraArgs    []string
	WorkDir      string
	SessionID    string
	Model        string
	WorktreeDirs []string

	// Codex-only.
	SandboxMode    string
	ApprovalPolicy string
}

// EnvVar is a key/value environment entry from workspace env presets.
type EnvVar struct {
	Key   string
	Value string
}

// EnvOptions captures environment injection concerns common to all CLI types.
type EnvOptions struct {
	WorkspaceEnv     []EnvVar
	AccountConfigDir string
	GitAuthorName    string
	GitAuthorEmail   string
}

// PermissionOptions is the CLI permission surface. For Codex, approval_policy
// is written to config.toml instead of represented as argv flags.
type PermissionOptions struct {
	Mode           string
	UserAllowedCSV string
	MCPAvailable   bool
}
