package agentproxy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/agentproxy/adapter"
	"github.com/niuniu-dev/niuniu/internal/sceneenv"
	"github.com/niuniu-dev/niuniu/internal/store"
)

type codexAppServerRuntime struct {
	command        string
	env            []string
	model          string
	sandboxMode    string
	approvalPolicy string
	runtimeRoots   []string
}

func (s *WorkspaceSession) runCodexAppServerTurn(ctx context.Context, workDir, content, msgId string) error {
	runtime, err := s.buildCodexAppServerRuntime(ctx, workDir)
	if err != nil {
		s.finishOneShotTurn(ctx, msgId, true, err.Error())
		return err
	}
	if err := s.ensureCodexAppServer(ctx, workDir, runtime); err != nil {
		slog.Error("codex app-server: ensure failed", "workspaceID", s.workspaceID, "workDir", workDir, "err", err)
		// running/status is loop-scoped (owned by SendLoop); only flag the turn error here.
		s.mu.Lock()
		s.lastTurnError = true
		s.mu.Unlock()
		s.handleEvent(ctx, ParsedEvent{Type: "result", IsError: true, Result: "Failed to start Codex app-server: " + err.Error()}, msgId)
		return err
	}

	s.procMu.Lock()
	client := s.codexApp
	threadID := s.codexThreadID
	s.procMu.Unlock()
	if client == nil || threadID == "" {
		err := fmt.Errorf("codex app-server thread is not initialized")
		s.finishOneShotTurn(ctx, msgId, true, err.Error())
		return err
	}

	params := codexAppServerTurnStartParams{
		ThreadID:       threadID,
		Input:          []map[string]any{{"type": "text", "text": content}},
		Cwd:            workDir,
		Model:          runtime.model,
		ApprovalPolicy: runtime.approvalPolicy,
		SandboxPolicy:  codexAppServerSandboxPolicy(runtime.sandboxMode, runtime.runtimeRoots),
		RuntimeRoots:   runtime.runtimeRoots,
	}
	if _, err := client.StartTurn(ctx, params); err != nil {
		s.finishOneShotTurn(ctx, msgId, true, "Codex turn failed to start: "+err.Error())
		return err
	}

	s.mu.Lock()
	s.lastActivityAt = time.Now() // baseline: starting the turn counts as activity
	s.mu.Unlock()
	// Same inactivity watchdog as the claude path: a wedged/lost app-server can
	// never block this turn forever.
	return s.waitForTurnComplete(ctx, "codex app-server")
}

func (s *WorkspaceSession) ensureCodexAppServer(ctx context.Context, workDir string, runtime codexAppServerRuntime) error {
	s.procMu.Lock()
	if s.alive && s.codexApp != nil && s.codexThreadID != "" {
		s.procMu.Unlock()
		return nil
	}
	s.procMu.Unlock()

	if info, err := os.Stat(workDir); err != nil {
		return fmt.Errorf("workspace directory not found: %w", err)
	} else if !info.IsDir() {
		return fmt.Errorf("workspace path is not a directory: %s", workDir)
	}

	app, err := startCodexAppServerClient(ctx, runtime.command, runtime.env)
	if err != nil {
		return err
	}

	s.mu.Lock()
	sessionID := s.sessionId
	s.mu.Unlock()

	var started codexAppServerThreadStartResponse
	if sessionID != "" {
		started, err = app.ResumeThread(ctx, codexAppServerThreadResumeParams{
			ThreadID:       sessionID,
			Cwd:            workDir,
			Model:          runtime.model,
			Sandbox:        runtime.sandboxMode,
			ApprovalPolicy: runtime.approvalPolicy,
			RuntimeRoots:   runtime.runtimeRoots,
			ExcludeTurns:   true,
		})
		if err != nil {
			slog.Warn("codex app-server: resume failed; starting fresh thread",
				"workspaceID", s.workspaceID, "threadID", sessionID, "err", err)
		}
	}
	if started.Thread.ID == "" {
		started, err = app.StartThread(ctx, codexAppServerThreadStartParams{
			Cwd:                workDir,
			Ephemeral:          false,
			SessionStartSource: "startup",
			Model:              runtime.model,
			Sandbox:            runtime.sandboxMode,
			ApprovalPolicy:     runtime.approvalPolicy,
			RuntimeRoots:       runtime.runtimeRoots,
		})
		if err != nil {
			_ = app.Close()
			return err
		}
	}

	threadID := started.Thread.ID
	if threadID == "" {
		threadID = started.Thread.SessionID
	}
	if threadID == "" {
		_ = app.Close()
		return fmt.Errorf("codex app-server returned empty thread id")
	}

	s.procMu.Lock()
	s.codexApp = app
	s.codexThreadID = threadID
	s.alive = true
	s.cmd = app.cmd
	s.stdin = nil
	s.reader = nil
	s.stderrBuf = nil
	s.workDir = workDir
	s.procMu.Unlock()

	s.mu.Lock()
	isNewSession := s.sessionId != threadID
	s.sessionId = threadID
	if isNewSession {
		s.driftChecked = false
	}
	s.workDir = workDir
	s.mu.Unlock()
	_ = s.q.UpdateSessionColumns(ctx, store.UpdateSessionColumnsParams{
		SessionID:     sql.NullString{String: threadID, Valid: true},
		SessionStatus: sql.NullString{String: string(StatusRunning), Valid: true},
		ID:            s.workspaceID,
	})
	_ = s.q.UpdateAgentStatus(ctx, store.UpdateAgentStatusParams{
		AgentPid:    sql.NullInt64{Int64: int64(app.cmd.Process.Pid), Valid: true},
		AgentStatus: sql.NullString{String: "running", Valid: true},
		ID:          s.workspaceID,
	})
	s.rebindTopLevelAgent(ctx)
	s.hub.Broadcast(s.workspaceID, NewOutputEvent(EventSystemInfo, "session started", "", "system", s.workspaceID))

	go s.codexAppServerEventLoop(ctx, app)
	return nil
}

func (s *WorkspaceSession) codexAppServerEventLoop(ctx context.Context, app *codexAppServerClient) {
	cliAdapter := s.cliAdapter
	if cliAdapter == nil {
		cliAdapter = adapter.CodexAdapter{}
	}
	for notif := range app.Events() {
		if len(notif.ID) > 0 {
			// Server->client request (approval).
			go s.handleCodexAppServerRequest(ctx, app, notif)
			continue
		}
		line, err := json.Marshal(map[string]any{
			"method": notif.Method,
			"params": json.RawMessage(notif.Params),
		})
		if err != nil {
			continue
		}
		events, parseErr := cliAdapter.ParseLine(string(line))
		if parseErr != nil {
			slog.Warn("codex app-server: parse error",
				"workspaceID", s.workspaceID, "method", notif.Method, "err", parseErr)
			continue
		}
		s.dispatchCodexEvents(ctx, events)
	}

	s.procMu.Lock()
	if s.codexApp == app {
		s.codexApp = nil
		s.alive = false
	}
	s.procMu.Unlock()
	_ = s.q.UpdateAgentStatus(ctx, store.UpdateAgentStatusParams{
		AgentPid:    sql.NullInt64{Valid: false},
		AgentStatus: sql.NullString{String: "idle", Valid: true},
		ID:          s.workspaceID,
	})

	s.mu.Lock()
	wasRunning := s.running
	msgId := s.turnMsgId
	s.mu.Unlock()
	// Surface the crash as a turn result when a real main turn is in flight.
	if wasRunning {
		s.handleEvent(ctx, ParsedEvent{
			Type:    "result",
			IsError: true,
			Result:  "Codex app-server exited unexpectedly",
		}, msgId)
		return
	}
	s.emitBgTaskNotify()
}

// dispatchCodexEvents routes parsed app-server events to the normal turn handler.
func (s *WorkspaceSession) dispatchCodexEvents(ctx context.Context, events []adapter.ParsedEvent) {
	s.mu.Lock()
	msgId := s.turnMsgId
	s.lastActivityAt = time.Now() // feed the turn inactivity watchdog
	s.mu.Unlock()
	for _, ev := range events {
		s.handleEvent(ctx, ev, msgId)
	}
}

func (s *WorkspaceSession) handleCodexAppServerRequest(ctx context.Context, app *codexAppServerClient, req codexAppServerNotification) {
	var args map[string]any
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &args)
	}
	if args == nil {
		args = map[string]any{}
	}

	tool := codexAppServerApprovalTool(req.Method)
	if tool == "" {
		slog.Warn("codex app-server: unsupported server request; returning empty result",
			"workspaceID", s.workspaceID, "method", req.Method)
		_ = app.respond(req.ID, map[string]any{})
		return
	}

	approved := false
	if s.permissionGate != nil {
		s.mu.Lock()
		sessionID := s.sessionId
		s.mu.Unlock()
		behavior, err := s.permissionGate.Request(ctx, s.workspaceID, s.ownerType, s.ownerID,
			sessionID, "codex:"+tool, args)
		if err != nil {
			slog.Warn("codex app-server approval: PermissionService.Request failed; denying",
				"workspaceID", s.workspaceID, "tool", tool, "err", err)
		} else if behavior == "allow" {
			approved = true
		}
	} else {
		slog.Warn("codex app-server approval: no PermissionGate wired; denying",
			"workspaceID", s.workspaceID, "tool", tool)
	}

	if err := app.respond(req.ID, codexAppServerApprovalResponse(req.Method, approved)); err != nil {
		slog.Warn("codex app-server approval: response write failed",
			"workspaceID", s.workspaceID, "method", req.Method, "err", err)
	}
}

func codexAppServerApprovalTool(method string) string {
	switch method {
	case "item/commandExecution/requestApproval", "execCommandApproval":
		return "exec"
	case "item/fileChange/requestApproval", "applyPatchApproval":
		return "patch"
	case "item/permissions/requestApproval":
		return "permissions"
	default:
		return ""
	}
}

func codexAppServerApprovalResponse(method string, approved bool) map[string]any {
	switch method {
	case "execCommandApproval", "applyPatchApproval":
		decision := "denied"
		if approved {
			decision = "approved"
		}
		return map[string]any{"decision": decision}
	case "item/permissions/requestApproval":
		if approved {
			return map[string]any{
				"permissions": map[string]any{
					"fileSystem": map[string]any{},
					"network":    map[string]any{"enabled": true},
				},
				"scope": "turn",
			}
		}
		return map[string]any{"permissions": map[string]any{}, "scope": "turn"}
	default:
		decision := "decline"
		if approved {
			decision = "accept"
		}
		return map[string]any{"decision": decision}
	}
}

func (s *WorkspaceSession) buildCodexAppServerRuntime(ctx context.Context, workDir string) (codexAppServerRuntime, error) {
	command, _, env, err := s.buildOneShotExec(ctx, workDir)
	if err != nil {
		return codexAppServerRuntime{}, err
	}
	out := codexAppServerRuntime{
		command:        command,
		env:            env,
		sandboxMode:    "danger-full-access",
		approvalPolicy: "never",
		runtimeRoots:   codexAppendRuntimeRoot(nil, workDir),
	}

	if cfg, cfgErr := s.q.GetWorkspaceCodexConfig(ctx, s.workspaceID); cfgErr == nil {
		if cfg.CodexSandboxMode != "" {
			out.sandboxMode = cfg.CodexSandboxMode
		}
		if cfg.CodexApprovalPolicy != "" {
			out.approvalPolicy = cfg.CodexApprovalPolicy
		}
	} else if !errors.Is(cfgErr, sql.ErrNoRows) {
		slog.Warn("codex app-server: failed to read codex config from workspace; using defaults",
			"workspaceID", s.workspaceID, "err", cfgErr)
	}

	wsEnvVars, envErr := sceneenv.Resolve(ctx, s.q, s.workspaceID)
	if envErr != nil {
		return codexAppServerRuntime{}, fmt.Errorf("fetch workspace env vars: %w", envErr)
	}
	for _, e := range wsEnvVars {
		if e.Key == "NIUNIU_MODEL" && e.Value != "" {
			out.model = e.Value
			s.mu.Lock()
			s.modelName = e.Value
			s.mu.Unlock()
		}
	}

	worktrees, wtErr := s.q.ListWorktrees(ctx, s.workspaceID)
	if wtErr != nil {
		slog.Warn("codex app-server: failed to list worktrees", "workspaceID", s.workspaceID, "err", wtErr)
	} else {
		for _, wt := range worktrees {
			out.runtimeRoots = codexAppendRuntimeRoot(out.runtimeRoots, wt.WorktreePath)
			repo, repoErr := s.q.GetRepository(ctx, wt.RepositoryID)
			if repoErr != nil {
				slog.Warn("codex app-server: failed to resolve repository path",
					"workspaceID", s.workspaceID, "repositoryID", wt.RepositoryID, "err", repoErr)
				continue
			}
			out.runtimeRoots = codexAppendRuntimeRoot(out.runtimeRoots, repo.Path)
		}
	}
	return out, nil
}

func codexAppendRuntimeRoot(roots []string, path string) []string {
	path = strings.TrimSpace(path)
	if path == "" {
		return roots
	}
	for _, existing := range roots {
		if existing == path {
			return roots
		}
	}
	return append(roots, path)
}

func codexAppServerSandboxPolicy(mode string, roots []string) map[string]any {
	if roots == nil {
		roots = []string{}
	}
	switch mode {
	case "read-only":
		return map[string]any{"type": "readOnly", "networkAccess": true}
	case "workspace-write":
		return map[string]any{
			"type":          "workspaceWrite",
			"networkAccess": true,
			"writableRoots": roots,
		}
	case "danger-full-access", "":
		return map[string]any{"type": "dangerFullAccess"}
	default:
		return nil
	}
}
