package adapter

import (
	"slices"
	"testing"
)

func TestClaudeAdapter_BuildSpawn(t *testing.T) {
	if (ClaudeAdapter{}).ProcessMode() != ProcessLongRunning {
		t.Fatalf("claude process mode = %q", (ClaudeAdapter{}).ProcessMode())
	}
	if got := (ClaudeAdapter{}).DisplayName(`C:\bin\claude.cmd`); got != "claude" {
		t.Fatalf("claude display name=%q", got)
	}
	cmd, args := (ClaudeAdapter{}).BuildSpawn(SpawnOptions{
		Command:      "claude-test",
		WorkDir:      "/workspace",
		SessionID:    "sid-1",
		Model:        "sonnet",
		WorktreeDirs: []string{"/workspace/.worktrees/a"},
		ExtraArgs:    []string{"--mcp-config", "/workspace/.mcp.json"},
	})
	if cmd != "claude-test" {
		t.Fatalf("cmd=%q want claude-test", cmd)
	}
	for _, want := range []string{"-p", "--verbose", "--output-format", "stream-json", "--input-format", "stream-json", "--include-partial-messages"} {
		if !slices.Contains(args, want) {
			t.Fatalf("args missing %q: %v", want, args)
		}
	}
	if !containsPair(args, "--add-dir", "/workspace/.worktrees/a") {
		t.Fatalf("args missing worktree add-dir: %v", args)
	}
	if !containsPair(args, "--resume", "sid-1") {
		t.Fatalf("args missing resume: %v", args)
	}
	if !containsPair(args, "--model", "sonnet") {
		t.Fatalf("args missing model: %v", args)
	}
	if !containsPair(args, "--mcp-config", "/workspace/.mcp.json") {
		t.Fatalf("args missing extra mcp config: %v", args)
	}
}

func TestCodexAdapter_BuildSpawnFreshAndResume(t *testing.T) {
	if (CodexAdapter{}).ProcessMode() != ProcessOneShot {
		t.Fatalf("codex process mode = %q", (CodexAdapter{}).ProcessMode())
	}
	if got := (CodexAdapter{}).DisplayName(""); got != "codex" {
		t.Fatalf("codex display name=%q", got)
	}
	freshCmd, freshArgs := (CodexAdapter{}).BuildSpawn(SpawnOptions{
		Command:      "codex-test",
		WorkDir:      "/workspace",
		Model:        "gpt-5",
		WorktreeDirs: []string{"/workspace/.worktrees/a"},
		SandboxMode:  "read-only",
	})
	if freshCmd != "codex-test" {
		t.Fatalf("fresh cmd=%q want codex-test", freshCmd)
	}
	for _, want := range []string{"exec", "--json", "--skip-git-repo-check", "-"} {
		if !slices.Contains(freshArgs, want) {
			t.Fatalf("fresh args missing %q: %v", want, freshArgs)
		}
	}
	if !containsPair(freshArgs, "-C", "/workspace") || !containsPair(freshArgs, "--sandbox", "read-only") {
		t.Fatalf("fresh args missing workdir/sandbox: %v", freshArgs)
	}
	if !containsPair(freshArgs, "--add-dir", "/workspace/.worktrees/a") {
		t.Fatalf("fresh args missing add-dir: %v", freshArgs)
	}

	_, resumeArgs := (CodexAdapter{}).BuildSpawn(SpawnOptions{SessionID: "sid-2", Model: "gpt-5"})
	if !slices.Contains(resumeArgs, "resume") || !containsPair(resumeArgs, "--model", "gpt-5") {
		t.Fatalf("resume args missing resume/model: %v", resumeArgs)
	}
	if resumeArgs[len(resumeArgs)-2] != "sid-2" || resumeArgs[len(resumeArgs)-1] != "-" {
		t.Fatalf("resume args should end with session id and stdin marker: %v", resumeArgs)
	}
}

func TestCodexAdapter_BuildSpawnBypassesWhenFullyOpened(t *testing.T) {
	_, defaultArgs := (CodexAdapter{}).BuildSpawn(SpawnOptions{WorkDir: "/workspace"})
	if !slices.Contains(defaultArgs, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("default args missing bypass flag: %v", defaultArgs)
	}

	_, freshArgs := (CodexAdapter{}).BuildSpawn(SpawnOptions{
		WorkDir:        "/workspace",
		SandboxMode:    "danger-full-access",
		ApprovalPolicy: "never",
	})
	if !slices.Contains(freshArgs, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("fresh args missing bypass flag: %v", freshArgs)
	}
	if slices.Contains(freshArgs, "--sandbox") {
		t.Fatalf("fresh bypass args should not also pass --sandbox: %v", freshArgs)
	}

	_, resumeArgs := (CodexAdapter{}).BuildSpawn(SpawnOptions{
		SessionID:      "sid-2",
		SandboxMode:    "danger-full-access",
		ApprovalPolicy: "never",
	})
	if !slices.Contains(resumeArgs, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("resume args missing bypass flag: %v", resumeArgs)
	}
}

func TestCodexAdapter_BuildSpawnDangerSandboxStillRequiresNeverApproval(t *testing.T) {
	_, args := (CodexAdapter{}).BuildSpawn(SpawnOptions{
		WorkDir:        "/workspace",
		SandboxMode:    "danger-full-access",
		ApprovalPolicy: "on-request",
	})
	if slices.Contains(args, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("must not bypass approvals unless approval_policy=never: %v", args)
	}
	if !containsPair(args, "--sandbox", "danger-full-access") {
		t.Fatalf("expected explicit danger sandbox without bypass: %v", args)
	}
}

func TestAdapterInjectEnv(t *testing.T) {
	env := (ClaudeAdapter{}).InjectEnv([]string{"PATH=/bin"}, EnvOptions{
		WorkspaceEnv: []EnvVar{
			{Key: "FOO", Value: "bar"},
			{Key: "NIUNIU_MODEL", Value: "ignored"},
		},
		AccountConfigDir: "/acct/claude",
		GitAuthorName:    "Ada",
		GitAuthorEmail:   "ada@example.com",
	})
	for _, want := range []string{"FOO=bar", "CLAUDE_CONFIG_DIR=/acct/claude", "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com"} {
		if !slices.Contains(env, want) {
			t.Fatalf("env missing %q: %v", want, env)
		}
	}
	if EnvHasKey(env, "NIUNIU_MODEL") {
		t.Fatalf("NIUNIU_* control env should be filtered: %v", env)
	}

	codexEnv := (CodexAdapter{}).InjectEnv([]string{"CODEX_HOME=/user"}, EnvOptions{AccountConfigDir: "/managed"})
	if got := envValueForKey(codexEnv, "CODEX_HOME"); got != "/user" {
		t.Fatalf("user CODEX_HOME should win, got %q", got)
	}
}

func TestClaudeAdapter_PermissionArgs(t *testing.T) {
	args := (ClaudeAdapter{}).PermissionArgs(PermissionOptions{
		Mode:           "default",
		UserAllowedCSV: "Bash,Read",
		MCPAvailable:   true,
	})
	if !containsPair(args, "--permission-mode", "default") {
		t.Fatalf("args missing permission mode: %v", args)
	}
	if !containsPair(args, "--permission-prompt-tool", claudePermissionPromptTool) {
		t.Fatalf("args missing prompt tool: %v", args)
	}
	if !containsPair(args, "--disallowedTools", "AskUserQuestion") {
		t.Fatalf("args missing AskUserQuestion disallow: %v", args)
	}
	if got := valueForKey(args, "--allowedTools"); got != "Read,Glob,Grep,LS,NotebookRead,TodoRead,Bash" {
		t.Fatalf("allowed tools=%q", got)
	}

	if got := (CodexAdapter{}).PermissionArgs(PermissionOptions{Mode: "on-request"}); len(got) != 0 {
		t.Fatalf("codex permission args should be config-only, got %v", got)
	}
}

func TestSanitizeAnthropicEnv(t *testing.T) {
	// Bearer token present: the stale x-api-key must be dropped, everything
	// else (incl. base URL and model overrides) kept.
	withToken := SanitizeAnthropicEnv([]string{
		"PATH=/bin",
		"ANTHROPIC_API_KEY=sk-ant-stale",
		"ANTHROPIC_AUTH_TOKEN=zhipu-real-key",
		"ANTHROPIC_BASE_URL=https://open.bigmodel.cn/api/anthropic",
	})
	if EnvHasKey(withToken, "ANTHROPIC_API_KEY") {
		t.Fatalf("ANTHROPIC_API_KEY should be stripped when auth token set: %v", withToken)
	}
	for _, want := range []string{"PATH=/bin", "ANTHROPIC_AUTH_TOKEN=zhipu-real-key", "ANTHROPIC_BASE_URL=https://open.bigmodel.cn/api/anthropic"} {
		if !slices.Contains(withToken, want) {
			t.Fatalf("env missing %q: %v", want, withToken)
		}
	}

	// No auth token: official-Claude api-key auth must be preserved untouched.
	noToken := SanitizeAnthropicEnv([]string{"PATH=/bin", "ANTHROPIC_API_KEY=sk-ant-real"})
	if !slices.Contains(noToken, "ANTHROPIC_API_KEY=sk-ant-real") {
		t.Fatalf("ANTHROPIC_API_KEY should survive when no auth token: %v", noToken)
	}

	// Empty auth-token value does not count as configured: leave api-key alone.
	emptyToken := SanitizeAnthropicEnv([]string{"ANTHROPIC_AUTH_TOKEN=", "ANTHROPIC_API_KEY=sk-ant-real"})
	if !slices.Contains(emptyToken, "ANTHROPIC_API_KEY=sk-ant-real") {
		t.Fatalf("empty auth token should not strip api-key: %v", emptyToken)
	}
}

func TestInjectEnvStripsConflictingAPIKey(t *testing.T) {
	// Host carries a leftover ANTHROPIC_API_KEY; the workspace preset configures a
	// third-party provider via ANTHROPIC_AUTH_TOKEN. The merged env handed to the
	// CLI must not still carry the host x-api-key (the "403 invalid api-key on some
	// computers" bug).
	env := (ClaudeAdapter{}).InjectEnv(
		[]string{"PATH=/bin", "ANTHROPIC_API_KEY=sk-ant-leftover"},
		EnvOptions{WorkspaceEnv: []EnvVar{
			{Key: "ANTHROPIC_AUTH_TOKEN", Value: "zhipu-real-key"},
			{Key: "ANTHROPIC_BASE_URL", Value: "https://open.bigmodel.cn/api/anthropic"},
		}},
	)
	if EnvHasKey(env, "ANTHROPIC_API_KEY") {
		t.Fatalf("conflicting ANTHROPIC_API_KEY should be stripped: %v", env)
	}
	if envValueForKey(env, "ANTHROPIC_AUTH_TOKEN") != "zhipu-real-key" {
		t.Fatalf("workspace auth token should be present: %v", env)
	}
}

func TestClaudeAdapter_StrictMCPPassedThrough(t *testing.T) {
	_, args := (ClaudeAdapter{}).BuildSpawn(SpawnOptions{
		Command:   "claude",
		ExtraArgs: []string{"--mcp-config", "/ws/.mcp.json", "--strict-mcp-config"},
	})
	found := false
	for _, a := range args {
		if a == "--strict-mcp-config" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("argv missing --strict-mcp-config: %v", args)
	}
}

func containsPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

func valueForKey(args []string, key string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key {
			return args[i+1]
		}
	}
	return ""
}

func envValueForKey(env []string, key string) string {
	prefix := key + "="
	for _, item := range env {
		if len(item) >= len(prefix) && item[:len(prefix)] == prefix {
			return item[len(prefix):]
		}
	}
	return ""
}
