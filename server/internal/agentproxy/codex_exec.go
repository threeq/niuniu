package agentproxy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/agentproxy/adapter"
	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/sceneenv"
)

// buildCodexOneShotExec assembles the command, argv and env for a Codex `exec`
// one-shot turn. Codex is the richest of the one-shot builders: it resolves a
// per-workspace sandbox/approval row, builds the niuniu-mcp argv, and writes
// .codex/config.toml before spawn.
//
// Registered in oneShotExecBuilders and also used as buildOneShotExec's
// nil-adapter fallback.
func (s *WorkspaceSession) buildCodexOneShotExec(ctx context.Context, workDir string) (string, []string, []string, error) {
	oneShotAdapter := adapter.Adapter(adapter.CodexAdapter{})
	if s.cliAdapter != nil && s.cliAdapter.Type() == adapter.TypeCodex {
		oneShotAdapter = s.cliAdapter
	}
	command := s.cfg.Agent.CodexCli.Command
	if command == "" {
		command = "codex"
	}
	extraArgs := append([]string{}, s.cfg.Agent.CodexCli.Args...)
	model := ""
	wsEnvVars, envErr := sceneenv.Resolve(ctx, s.q, s.workspaceID)
	if envErr != nil {
		return "", nil, nil, fmt.Errorf("fetch workspace env vars: %w", envErr)
	}
	workspaceEnv := make([]adapter.EnvVar, 0, len(wsEnvVars))
	for _, e := range wsEnvVars {
		workspaceEnv = append(workspaceEnv, adapter.EnvVar{Key: e.Key, Value: e.Value})
		switch e.Key {
		case "NIUNIU_AGENT_COMMAND":
			if e.Value != "" {
				command = e.Value
			}
		case "NIUNIU_AGENT_ARGS":
			if e.Value != "" {
				extraArgs = strings.Fields(e.Value)
			}
		case "NIUNIU_MODEL":
			model = e.Value
			if model != "" {
				s.mu.Lock()
				s.modelName = model
				s.mu.Unlock()
			}
		}
	}

	// Resolve per-workspace sandbox + approval settings (B1). Default to full
	// Codex bypass because niuniu provides the outer workspace isolation.
	sandboxMode := "danger-full-access"
	approvalPolicy := "never"
	if cfg, cfgErr := s.q.GetWorkspaceCodexConfig(ctx, s.workspaceID); cfgErr == nil {
		if cfg.CodexSandboxMode != "" {
			sandboxMode = cfg.CodexSandboxMode
		}
		if cfg.CodexApprovalPolicy != "" {
			approvalPolicy = cfg.CodexApprovalPolicy
		}
	} else if !errors.Is(cfgErr, sql.ErrNoRows) {
		slog.Warn("codex chat: failed to read codex config from workspace; using defaults",
			"workspaceID", s.workspaceID, "err", cfgErr)
	}

	s.mu.Lock()
	sessionID := s.sessionId
	s.mu.Unlock()

	var worktreeDirs []string
	worktrees, wtErr := s.q.ListWorktrees(ctx, s.workspaceID)
	if wtErr != nil {
		slog.Warn("codex chat: failed to list worktrees", "workspaceID", s.workspaceID, "err", wtErr)
	} else {
		for _, wt := range worktrees {
			if _, err := os.Stat(wt.WorktreePath); err != nil {
				slog.Warn("codex chat: worktree path missing", "workspaceID", s.workspaceID, "path", wt.WorktreePath, "err", err)
			}
			worktreeDirs = append(worktreeDirs, wt.WorktreePath)
		}
	}

	var mcpOpts *config.MCPGenerateOptions
	if s.mcpWriter != nil {
		projectID, _ := s.q.GetProjectIDForWorkspace(ctx, s.workspaceID)
		opts := config.MCPGenerateOptions{
			ProjectID:           projectID,
			WorkspaceID:         s.workspaceID,
			InboxDir:            filepath.Join(workDir, ".team", "inboxes"),
			SessionToken:        s.sessionToken,
			CodexSandboxMode:    sandboxMode,
			CodexApprovalPolicy: approvalPolicy,
		}
		if mcpArgs, err := s.mcpWriter.GenerateCodexConfigArgs(opts); err != nil {
			slog.Warn("codex chat: build codex mcp config args failed", "workspaceID", s.workspaceID, "err", err)
		} else {
			extraArgs = append(extraArgs, mcpArgs...)
		}
		mcpOpts = &opts
	}

	// Per-account CODEX_HOME switching removed; codex uses the host's global
	// ~/.codex/.
	var accountConfigDir string
	gitName, gitEmail := s.resolveGitAuthorEnv(ctx)
	env := oneShotAdapter.InjectEnv(os.Environ(), adapter.EnvOptions{
		WorkspaceEnv:     workspaceEnv,
		AccountConfigDir: accountConfigDir,
		GitAuthorName:    gitName,
		GitAuthorEmail:   gitEmail,
	})

	if s.mcpWriter != nil && mcpOpts != nil {
		if err := s.mcpWriter.GenerateCodexConfigToml(workDir, *mcpOpts); err != nil {
			slog.Warn("codex chat: write .codex/config.toml failed", "workspaceID", s.workspaceID, "err", err)
		}
	}
	command, args := oneShotAdapter.BuildSpawn(adapter.SpawnOptions{
		Command:        command,
		ExtraArgs:      extraArgs,
		WorkDir:        workDir,
		SessionID:      sessionID,
		Model:          model,
		WorktreeDirs:   worktreeDirs,
		SandboxMode:    sandboxMode,
		ApprovalPolicy: approvalPolicy,
	})
	return command, args, env, nil
}
