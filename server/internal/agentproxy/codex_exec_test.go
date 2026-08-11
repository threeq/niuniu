package agentproxy

import (
	"context"
	"slices"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/store"
)

func TestBuildCodexExec_UsesCurrentCLIFlags(t *testing.T) {
	q := setupDispatchDB(t)
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name:      "codex-argv-test",
		Path:      t.TempDir(),
		Status:    "created",
		OwnerType: "user",
		OwnerID:   1,
		CliType:   "codex",
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	s := &WorkspaceSession{
		workspaceID: ws.ID,
		q:           q,
		cfg: &config.Config{
			Agent: config.AgentConfig{
				CodexCli: config.CodexCliConfig{Command: "codex"},
			},
		},
	}

	command, args, env, err := s.buildOneShotExec(context.Background(), ws.Path)
	if err != nil {
		t.Fatalf("buildOneShotExec: %v", err)
	}
	if command != "codex" {
		t.Fatalf("command = %q, want codex", command)
	}
	if slices.Contains(args, "--ask-for-approval") {
		t.Fatalf("args include unsupported current Codex flag --ask-for-approval: %v", args)
	}
	if !slices.Contains(args, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("args missing full-bypass flag: %v", args)
	}
	if !containsArgPair(args, "-C", ws.Path) {
		t.Fatalf("args missing -C workspace path: %v", args)
	}
	if !slices.Contains(args, "--json") || !slices.Contains(args, "--skip-git-repo-check") {
		t.Fatalf("args missing required noninteractive flags: %v", args)
	}
	if args[len(args)-1] != "-" {
		t.Fatalf("last arg = %q, want stdin marker '-': %v", args[len(args)-1], args)
	}
	if codexExecTestHasEnvKey(env, "CODEX_HOME") {
		t.Fatalf("env must not override CODEX_HOME by default; it would hide user auth: %v", env)
	}
}

func TestBuildCodexExec_ResumeKeepsSkipGitRepoCheck(t *testing.T) {
	q := setupDispatchDB(t)
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name:      "codex-resume-argv-test",
		Path:      t.TempDir(),
		Status:    "created",
		OwnerType: "user",
		OwnerID:   1,
		CliType:   "codex",
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	s := &WorkspaceSession{
		workspaceID: ws.ID,
		sessionId:   "session-123",
		q:           q,
		cfg: &config.Config{
			Agent: config.AgentConfig{
				CodexCli: config.CodexCliConfig{Command: "codex"},
			},
		},
	}

	_, args, _, err := s.buildOneShotExec(context.Background(), ws.Path)
	if err != nil {
		t.Fatalf("buildOneShotExec: %v", err)
	}
	if !slices.Contains(args, "--skip-git-repo-check") {
		t.Fatalf("resume args missing --skip-git-repo-check: %v", args)
	}
	if args[len(args)-2] != "session-123" || args[len(args)-1] != "-" {
		t.Fatalf("resume args should end with session id and stdin marker: %v", args)
	}
}

func containsArgPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

func codexExecTestHasEnvKey(env []string, key string) bool {
	prefix := key + "="
	for _, item := range env {
		if len(item) >= len(prefix) && item[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

func codexExecTestEnvValue(env []string, key string) string {
	prefix := key + "="
	for _, item := range env {
		if len(item) >= len(prefix) && item[:len(prefix)] == prefix {
			return item[len(prefix):]
		}
	}
	return ""
}

// stubMCPWriter is a fake MCPConfigWriter for codex exec tests.
type stubMCPWriter struct {
	configArgs []string
	gotOpts    config.MCPGenerateOptions
}

func (w *stubMCPWriter) Generate(string, config.MCPGenerateOptions, []string, string) (*MCPGenerateResult, error) {
	return nil, nil
}

func (w *stubMCPWriter) GenerateClaudeSettings(string) error { return nil }

func (w *stubMCPWriter) GenerateCodexConfigToml(string, config.MCPGenerateOptions) error { return nil }

func (w *stubMCPWriter) GenerateCodexConfigArgs(opts config.MCPGenerateOptions) ([]string, error) {
	w.gotOpts = opts
	return w.configArgs, nil
}

func (w *stubMCPWriter) SetWorkspaceKBReadonly(string, []string) error { return nil }
func (w *stubMCPWriter) NiuniuMcpServer(config.MCPGenerateOptions) (config.McpServerEntry, error) {
	return config.McpServerEntry{}, nil
}

// TestBuildCodexExec_ReadsSandboxFromWorkspace verifies that B1's per-workspace
// codex_sandbox_mode column flows through to the --sandbox CLI flag.
func TestBuildCodexExec_ReadsSandboxFromWorkspace(t *testing.T) {
	q := setupDispatchDB(t)
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name: "codex-sandbox-test", Path: t.TempDir(), Status: "created",
		OwnerType: "user", OwnerID: 1, CliType: "codex",
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if err := q.SetWorkspaceCodexSandbox(context.Background(), store.SetWorkspaceCodexSandboxParams{
		CodexSandboxMode:    "read-only",
		CodexApprovalPolicy: "on-request",
		ID:                  ws.ID,
	}); err != nil {
		t.Fatalf("SetWorkspaceCodexSandbox: %v", err)
	}

	s := &WorkspaceSession{
		workspaceID: ws.ID, q: q,
		cfg: &config.Config{Agent: config.AgentConfig{CodexCli: config.CodexCliConfig{Command: "codex"}}},
	}
	_, args, _, err := s.buildOneShotExec(context.Background(), ws.Path)
	if err != nil {
		t.Fatalf("buildOneShotExec: %v", err)
	}
	if !containsArgPair(args, "--sandbox", "read-only") {
		t.Errorf("expected --sandbox read-only from workspace row; args=%v", args)
	}
	// approval_policy goes via config overrides, NOT --ask-for-approval CLI flag.
	if slices.Contains(args, "--ask-for-approval") {
		t.Errorf("must NOT pass --ask-for-approval CLI flag; codex 0.x does not support it. args=%v", args)
	}
}

func TestBuildCodexExec_FullyOpenedWorkspaceUsesBypassFlag(t *testing.T) {
	q := setupDispatchDB(t)
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name: "codex-open-test", Path: t.TempDir(), Status: "created",
		OwnerType: "user", OwnerID: 1, CliType: "codex",
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if err := q.SetWorkspaceCodexSandbox(context.Background(), store.SetWorkspaceCodexSandboxParams{
		CodexSandboxMode:    "danger-full-access",
		CodexApprovalPolicy: "never",
		ID:                  ws.ID,
	}); err != nil {
		t.Fatalf("SetWorkspaceCodexSandbox: %v", err)
	}

	s := &WorkspaceSession{
		workspaceID: ws.ID, q: q,
		cfg: &config.Config{Agent: config.AgentConfig{CodexCli: config.CodexCliConfig{Command: "codex"}}},
	}
	_, args, _, err := s.buildOneShotExec(context.Background(), ws.Path)
	if err != nil {
		t.Fatalf("buildOneShotExec: %v", err)
	}
	if !slices.Contains(args, "--dangerously-bypass-approvals-and-sandbox") {
		t.Errorf("expected bypass flag for fully opened workspace; args=%v", args)
	}
	if slices.Contains(args, "--sandbox") {
		t.Errorf("bypass mode should not also pass --sandbox; args=%v", args)
	}
}

func TestBuildCodexExec_InlinesMCPConfigArgs(t *testing.T) {
	q := setupDispatchDB(t)
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name: "codex-mcp-args-test", Path: t.TempDir(), Status: "created",
		OwnerType: "user", OwnerID: 1, CliType: "codex",
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	writer := &stubMCPWriter{configArgs: []string{
		"--config", `mcp_servers.niuniu.command="niuniu-mcp"`,
		"--config", `mcp_servers.niuniu.args=["--workspace-id", "1"]`,
	}}
	s := &WorkspaceSession{
		workspaceID: ws.ID, q: q, sessionToken: "tok-123", mcpWriter: writer,
		cfg: &config.Config{Agent: config.AgentConfig{CodexCli: config.CodexCliConfig{Command: "codex"}}},
	}
	_, args, _, err := s.buildOneShotExec(context.Background(), ws.Path)
	if err != nil {
		t.Fatalf("buildOneShotExec: %v", err)
	}
	execIdx := slices.Index(args, "exec")
	configIdx := slices.Index(args, "--config")
	if configIdx < 0 || execIdx < 0 || configIdx > execIdx {
		t.Fatalf("mcp config overrides must be passed before exec: %v", args)
	}
	if writer.gotOpts.WorkspaceID != ws.ID || writer.gotOpts.SessionToken != "tok-123" {
		t.Fatalf("mcp opts missing workspace/token: %+v", writer.gotOpts)
	}
}

// TestBuildCodexExec_DefaultsWhenSandboxUnset verifies niuniu's default Codex
// launch trusts the outer workspace sandbox.
func TestBuildCodexExec_DefaultsWhenSandboxUnset(t *testing.T) {
	q := setupDispatchDB(t)
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name: "codex-defaults-test", Path: t.TempDir(), Status: "created",
		OwnerType: "user", OwnerID: 1, CliType: "codex",
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	s := &WorkspaceSession{
		workspaceID: ws.ID, q: q,
		cfg: &config.Config{Agent: config.AgentConfig{CodexCli: config.CodexCliConfig{Command: "codex"}}},
	}
	_, args, _, err := s.buildOneShotExec(context.Background(), ws.Path)
	if err != nil {
		t.Fatalf("buildOneShotExec: %v", err)
	}
	if !slices.Contains(args, "--dangerously-bypass-approvals-and-sandbox") {
		t.Errorf("default sandbox must bypass Codex approvals/sandbox; args=%v", args)
	}
	if slices.Contains(args, "--sandbox") {
		t.Errorf("bypass mode should not also pass --sandbox; args=%v", args)
	}
}

// TestBuildCodexExec_InjectsCodexHomeWhenBound verifies A4 CODEX_HOME injection
// when a codex account resolves for the workspace.
