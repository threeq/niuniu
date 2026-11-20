package agentproxy

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/agentproxy/adapter"
	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/sceneenv"
)

// buildQwenOneShotExec assembles the command, argv and env for a Qwen Code
// one-shot turn. Qwen Code (niuniu's third agent engine) is a Gemini-CLI fork:
// each headless invocation is a single turn fed on stdin by runOneShotTurn,
// with continuity carried by --resume <sessionId>. Unlike Codex it has no
// managed-account home dir or sandbox config.toml, so this builder is a trimmed
// sibling of buildOneShotExec: resolve workspace env (provider OPENAI_*/DashScope
// vars, NIUNIU_* control keys), worktree dirs, git identity, permission mode,
// then delegate argv/env shaping to QwenAdapter.
func (s *WorkspaceSession) buildQwenOneShotExec(ctx context.Context, workDir string) (string, []string, []string, error) {
	qwenAdapter := adapter.QwenAdapter{}

	command := s.cfg.Agent.QwenCli.Command
	if command == "" {
		command = "qwen"
	}
	extraArgs := append([]string{}, s.cfg.Agent.QwenCli.Args...)

	model := ""
	permissionMode := ""
	wsEnvVars, envErr := sceneenv.Resolve(ctx, s.q, s.workspaceID)
	if envErr != nil {
		return "", nil, nil, fmt.Errorf("fetch workspace env vars: %w", envErr)
	}
	// Mirror the claude path in ensureProcess: record the provider this spawn
	// resolved, so non-claude workspaces get the same pill/state tracking.
	s.recordActiveProvider(ctx)
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
		case "NIUNIU_PERMISSION_MODE":
			permissionMode = e.Value
		case "NIUNIU_MODEL":
			if e.Value != "" {
				model = e.Value
				s.mu.Lock()
				s.modelName = e.Value
				s.mu.Unlock()
			}
		}
	}

	s.mu.Lock()
	sessionID := s.sessionId
	s.mu.Unlock()

	var worktreeDirs []string
	if worktrees, wtErr := s.q.ListWorktrees(ctx, s.workspaceID); wtErr != nil {
		slog.Warn("qwen chat: failed to list worktrees", "workspaceID", s.workspaceID, "err", wtErr)
	} else {
		for _, wt := range worktrees {
			if _, err := os.Stat(wt.WorktreePath); err != nil {
				slog.Warn("qwen chat: worktree path missing", "workspaceID", s.workspaceID, "path", wt.WorktreePath, "err", err)
			}
			worktreeDirs = append(worktreeDirs, wt.WorktreePath)
		}
	}

	gitName, gitEmail := s.resolveGitAuthorEnv(ctx)

	env := qwenAdapter.InjectEnv(os.Environ(), adapter.EnvOptions{
		WorkspaceEnv:   workspaceEnv,
		GitAuthorName:  gitName,
		GitAuthorEmail: gitEmail,
	})

	// MCP collaboration: Qwen Code reads the project-root .mcp.json (its
	// "project" MCP scope) — the same file the Claude path writes — so the
	// agent gets the niuniu tool surface (board / memory / documents / …).
	if s.mcpWriter != nil {
		projectID, _ := s.q.GetProjectIDForWorkspace(ctx, s.workspaceID)
		mcpOpts := config.MCPGenerateOptions{
			ProjectID:    projectID,
			WorkspaceID:  s.workspaceID,
			InboxDir:     filepath.Join(workDir, ".team", "inboxes"),
			SessionToken: s.sessionToken,
		}
		if _, err := s.mcpWriter.Generate(workDir, mcpOpts, nil, ""); err != nil {
			slog.Warn("qwen chat: write .mcp.json failed", "workspaceID", s.workspaceID, "err", err)
		}
		// Local scope keeps the server out of the project-trust approval path
		// that gates Qwen's project-level .mcp.json.
		if err := s.mcpWriter.WriteJSONMCPServer(filepath.Join(workDir, ".qwen", "settings.local.json"), mcpOpts); err != nil {
			slog.Warn("qwen chat: write .qwen/settings.local.json failed", "workspaceID", s.workspaceID, "err", err)
		}
	}

	// Permission flags (--yolo for auto-run modes) so a headless, non-TTY qwen
	// run does not hang on an interactive approval it can never receive.
	extraArgs = append(extraArgs, qwenAdapter.PermissionArgs(adapter.PermissionOptions{Mode: permissionMode})...)

	command, args := qwenAdapter.BuildSpawn(adapter.SpawnOptions{
		Command:      command,
		ExtraArgs:    extraArgs,
		WorkDir:      workDir,
		SessionID:    sessionID,
		Model:        model,
		WorktreeDirs: worktreeDirs,
	})
	return command, args, env, nil
}
