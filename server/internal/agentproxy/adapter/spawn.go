package adapter

import (
	"path/filepath"
	"strings"
)

const (
	// AutohostMode is niuniu's permission mode that maps to Claude's
	// bypassPermissions at the CLI boundary. The in-session watchdog remains in
	// agentproxy.
	AutohostMode = "autohost"

	claudePermissionPromptTool = "mcp__niuniu__niuniu_permission_prompt"
)

var defaultSafeTools = []string{"Read", "Glob", "Grep", "LS", "NotebookRead", "TodoRead"}

func (ClaudeAdapter) ProcessMode() ProcessMode { return ProcessLongRunning }

func (CodexAdapter) ProcessMode() ProcessMode { return ProcessOneShot }

func (ClaudeAdapter) DisplayName(command string) string {
	return cliBaseName(command, "claude")
}

func (CodexAdapter) DisplayName(command string) string {
	return cliBaseName(command, "codex")
}

func (ClaudeAdapter) BuildSpawn(opts SpawnOptions) (string, []string) {
	command := opts.Command
	if command == "" {
		command = "claude"
	}
	args := []string{
		"-p",
		"--verbose",
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--include-partial-messages",
	}
	for _, dir := range opts.WorktreeDirs {
		args = append(args, "--add-dir", dir)
	}
	if opts.SessionID != "" {
		args = append(args, "--resume", opts.SessionID)
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	args = append(args, opts.ExtraArgs...)
	return command, args
}

func cliBaseName(command, fallback string) string {
	if command == "" {
		return fallback
	}
	base := filepath.Base(command)
	base = strings.TrimSuffix(base, ".exe")
	base = strings.TrimSuffix(base, ".cmd")
	if base == "" || base == "." {
		return fallback
	}
	return base
}

func (CodexAdapter) BuildSpawn(opts SpawnOptions) (string, []string) {
	command := opts.Command
	if command == "" {
		command = "codex"
	}
	sandbox := opts.SandboxMode
	if sandbox == "" {
		sandbox = "danger-full-access"
	}
	approvalPolicy := opts.ApprovalPolicy
	if approvalPolicy == "" {
		approvalPolicy = "never"
	}
	bypassPermissions := sandbox == "danger-full-access" && approvalPolicy == "never"
	args := append([]string{}, opts.ExtraArgs...)
	if opts.SessionID != "" {
		args = append(args, "exec", "resume", "--json", "--skip-git-repo-check")
		if bypassPermissions {
			args = append(args, "--dangerously-bypass-approvals-and-sandbox")
		}
		if opts.Model != "" {
			args = append(args, "--model", opts.Model)
		}
		args = append(args, opts.SessionID)
	} else {
		args = append(args, "exec", "--json", "--skip-git-repo-check", "-C", opts.WorkDir)
		if bypassPermissions {
			args = append(args, "--dangerously-bypass-approvals-and-sandbox")
		} else {
			args = append(args, "--sandbox", sandbox)
		}
		if opts.Model != "" {
			args = append(args, "--model", opts.Model)
		}
		for _, dir := range opts.WorktreeDirs {
			args = append(args, "--add-dir", dir)
		}
	}
	args = append(args, "-")
	return command, args
}

func (ClaudeAdapter) InjectEnv(base []string, opts EnvOptions) []string {
	return injectCLIEnv(base, opts, "CLAUDE_CONFIG_DIR")
}

func (CodexAdapter) InjectEnv(base []string, opts EnvOptions) []string {
	return injectCLIEnv(base, opts, "CODEX_HOME")
}

func injectCLIEnv(base []string, opts EnvOptions, accountKey string) []string {
	out := append([]string{}, base...)
	for _, e := range opts.WorkspaceEnv {
		if strings.HasPrefix(e.Key, "NIUNIU_") {
			continue
		}
		out = append(out, e.Key+"="+e.Value)
	}
	// accountKey is empty for adapters with no niuniu-managed home dir (Qwen):
	// guard so a stray AccountConfigDir can never produce a malformed "=value".
	if accountKey != "" && opts.AccountConfigDir != "" && !EnvHasKey(out, accountKey) {
		out = append(out, accountKey+"="+opts.AccountConfigDir)
	}
	if opts.GitAuthorName != "" && opts.GitAuthorEmail != "" && !EnvHasKey(out, "GIT_AUTHOR_NAME") {
		out = append(out,
			"GIT_AUTHOR_NAME="+opts.GitAuthorName,
			"GIT_AUTHOR_EMAIL="+opts.GitAuthorEmail,
			"GIT_COMMITTER_NAME="+opts.GitAuthorName,
			"GIT_COMMITTER_EMAIL="+opts.GitAuthorEmail,
		)
	}
	return SanitizeAnthropicEnv(out)
}

// anthropicAuthTokenKey / anthropicAPIKeyKey are the two mutually-exclusive
// Claude credential env vars: ANTHROPIC_API_KEY becomes the x-api-key header,
// ANTHROPIC_AUTH_TOKEN becomes the Authorization: Bearer header.
const (
	anthropicAuthTokenKey = "ANTHROPIC_AUTH_TOKEN"
	anthropicAPIKeyKey    = "ANTHROPIC_API_KEY"
)

// SanitizeAnthropicEnv resolves the ANTHROPIC_AUTH_TOKEN vs ANTHROPIC_API_KEY
// collision in a KEY=VALUE env slice. When a non-empty ANTHROPIC_AUTH_TOKEN is
// present (a third-party provider like 智谱/Kimi/DeepSeek configured via
// ANTHROPIC_BASE_URL authenticates with this Bearer token), every
// ANTHROPIC_API_KEY entry is dropped so it cannot also be sent as x-api-key.
//
// Without this, a stale ANTHROPIC_API_KEY inherited from the host environment
// (e.g. left over from a prior official-Claude setup) is forwarded to the
// third-party gateway alongside the Bearer token and the gateway rejects the
// request with "403 invalid api-key". Because only machines carrying that
// leftover key fail, the symptom is the classic "works on some computers, 403
// on others" after configuring a provider.
//
// When no auth token is set the slice is returned unchanged, so official-Claude
// api-key authentication keeps working. Pure: never mutates the argument.
func SanitizeAnthropicEnv(env []string) []string {
	if !envHasNonEmptyValue(env, anthropicAuthTokenKey) {
		return env
	}
	prefix := anthropicAPIKeyKey + "="
	out := make([]string, 0, len(env))
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// envHasNonEmptyValue reports whether env contains KEY= with a non-empty value.
func envHasNonEmptyValue(env []string, key string) bool {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) && len(e) > len(prefix) {
			return true
		}
	}
	return false
}

// EnvHasKey reports whether env already contains KEY=.
func EnvHasKey(env []string, key string) bool {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return true
		}
	}
	return false
}

func (ClaudeAdapter) PermissionArgs(opts PermissionOptions) []string {
	return BuildClaudePermissionArgs(opts.Mode, opts.UserAllowedCSV, opts.MCPAvailable)
}

func (CodexAdapter) PermissionArgs(PermissionOptions) []string {
	return nil
}

// BuildClaudePermissionArgs returns the CLI flags governing Claude permissions:
// --permission-mode, optional --permission-prompt-tool, and --allowedTools
// merged from defaults + user-supplied CSV.
func BuildClaudePermissionArgs(mode string, userAllowedCSV string, mcpAvailable bool) []string {
	if mode == "" {
		mode = "default"
	}
	cliMode := mode
	if cliMode == AutohostMode {
		cliMode = "bypassPermissions"
	}
	out := []string{"--permission-mode", cliMode}
	if (mode == "default" || mode == "acceptEdits") && mcpAvailable {
		out = append(out, "--permission-prompt-tool", claudePermissionPromptTool)
	}
	if mcpAvailable {
		out = append(out, "--disallowedTools", "AskUserQuestion")
	}
	if cliMode != "bypassPermissions" {
		if merged := MergeAllowedTools(defaultSafeTools, userAllowedCSV); merged != "" {
			out = append(out, "--allowedTools", merged)
		}
	}
	return out
}

// MergeAllowedTools merges a default safe-tools slice with a comma-separated
// user-supplied list, deduplicating while preserving insertion order.
func MergeAllowedTools(defaults []string, userCSV string) string {
	seen := map[string]struct{}{}
	var out []string
	push := func(t string) {
		t = strings.TrimSpace(t)
		if t == "" {
			return
		}
		if _, dup := seen[t]; dup {
			return
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	for _, t := range defaults {
		push(t)
	}
	for _, t := range strings.Split(userCSV, ",") {
		push(t)
	}
	return strings.Join(out, ",")
}
