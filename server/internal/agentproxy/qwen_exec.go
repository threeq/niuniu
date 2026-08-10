package agentproxy

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/agentproxy/adapter"
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

	var gitName, gitEmail string
	if gitUID := s.effectiveGitUserID(ctx); s.gitIdentity != nil && gitUID > 0 {
		if name, email, err := s.gitIdentity.ResolveNameEmail(ctx, gitUID); err == nil && name != "" && email != "" {
			gitName, gitEmail = name, email
		}
	}

	env := qwenAdapter.InjectEnv(os.Environ(), adapter.EnvOptions{
		WorkspaceEnv:   workspaceEnv,
		GitAuthorName:  gitName,
		GitAuthorEmail: gitEmail,
	})

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
