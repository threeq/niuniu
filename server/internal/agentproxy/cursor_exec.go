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

// buildCursorOneShotExec assembles the command, argv and env for a Cursor agent
// one-shot turn. cursor-agent (niuniu's sixth agent engine) runs one turn per
// invocation with the prompt fed on stdin by runOneShotTurn and continuity
// carried by --resume <chatId>. Like Qwen it has no niuniu-managed account home
// dir or sandbox config file, so this is a trimmed sibling of buildOneShotExec:
// resolve workspace env (provider vars + NIUNIU_* control keys), git identity
// and permission mode, then delegate argv/env shaping to CursorAdapter.
//
// WorktreeDirs are resolved only to log a missing path early (a stale worktree
// is a common cause of a confusing agent failure); cursor-agent takes a single
// --workspace root rather than repeatable --add-dir flags, and the workspace dir
// already contains every worktree beneath it.
func (s *WorkspaceSession) buildCursorOneShotExec(ctx context.Context, workDir string) (string, []string, []string, error) {
	cursorAdapter := adapter.CursorAdapter{}

	command := s.cfg.Agent.CursorCli.Command
	if command == "" {
		command = "cursor-agent"
	}
	extraArgs := append([]string{}, s.cfg.Agent.CursorCli.Args...)

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

	if worktrees, wtErr := s.q.ListWorktrees(ctx, s.workspaceID); wtErr != nil {
		slog.Warn("cursor chat: failed to list worktrees", "workspaceID", s.workspaceID, "err", wtErr)
	} else {
		for _, wt := range worktrees {
			if _, err := os.Stat(wt.WorktreePath); err != nil {
				slog.Warn("cursor chat: worktree path missing", "workspaceID", s.workspaceID, "path", wt.WorktreePath, "err", err)
			}
		}
	}

	gitName, gitEmail := s.resolveGitAuthorEnv(ctx)

	env := cursorAdapter.InjectEnv(os.Environ(), adapter.EnvOptions{
		WorkspaceEnv:   workspaceEnv,
		GitAuthorName:  gitName,
		GitAuthorEmail: gitEmail,
	})

	// Permission flags (--trust --force for auto-run modes) so a headless,
	// non-TTY cursor-agent run does not hang on a trust/approval prompt it can
	// never receive.
	extraArgs = append(extraArgs, cursorAdapter.PermissionArgs(adapter.PermissionOptions{Mode: permissionMode})...)

	command, args := cursorAdapter.BuildSpawn(adapter.SpawnOptions{
		Command:   command,
		ExtraArgs: extraArgs,
		WorkDir:   workDir,
		SessionID: sessionID,
		Model:     model,
	})
	return command, args, env, nil
}
