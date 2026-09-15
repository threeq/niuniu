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
	// configArgs 携带 -c 覆盖（如 provider 中转的 model_provider/wire_api），
	// 需要置于子命令之前传给 codex 根命令。
	configArgs []string
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

	app, err := startCodexAppServerClient(ctx, runtime.command, runtime.configArgs, runtime.env)
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
	// Burst batching: while the channel has buffered notifications, drain up to
	// codexPersistBatchLimit of them into ONE persist-batch window — N event
	// writes then commit ONCE instead of N times (each commit is a round-trip
	// to PostgreSQL; per-row commits were the consumer bottleneck that
	// saturated the events channel). Idle streams dispatch immediately (no
	// added latency).
	closed := false
	for !closed {
		// Blocking wait for the next notification — the loop's heartbeat. It
		// ONLY ends when the channel is truly CLOSED (process exited); a
		// momentarily-empty channel below merely ends the current batch.
		// (Regression: the old drain's select-default set closed=true on an
		// idle gap, killing the whole loop — and the exit path then reported
		// the still-alive app-server as "exited unexpectedly" — right after
		// thread/started, i.e. on every session start.)
		notif, ok := <-app.Events()
		if !ok {
			break
		}
		batch := []codexAppServerNotification{notif}
	drain:
		for len(batch) < codexPersistBatchLimit {
			select {
			case n, ok := <-app.Events():
				if !ok {
					closed = true
					break drain
				}
				batch = append(batch, n)
			default:
				break drain // momentarily empty — end the BATCH, not the loop
			}
		}
		s.flushCodexBatch(ctx, app, cliAdapter, batch)
	}

	// The app-server process exited (events channel closed): clear the stale
	// session plumbing and surface the crash as a turn result when a real main
	// turn was in flight.
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

	// The REAL reason lives on stderr (bad flag / broken config / auth error
	// all print there — stdout carries only the JSON protocol). Log it and
	// attach the tail to the surfaced error so the user sees why, not just
	// that it died.
	stderrTail := strings.TrimSpace(app.StderrTail(800))
	if stderrTail != "" {
		slog.Error("codex app-server exited — stderr tail",
			"workspace_id", s.workspaceID, "stderr", truncate(stderrTail, 800))
	}

	s.mu.Lock()
	wasRunning := s.running
	msgId := s.turnMsgId
	s.mu.Unlock()
	if wasRunning {
		result := "Codex app-server exited unexpectedly"
		if stderrTail != "" {
			result += "：" + truncate(stderrTail, 400)
		}
		s.handleEvent(ctx, ParsedEvent{
			Type:    "result",
			IsError: true,
			Result:  result,
		}, msgId)
		return
	}
	s.emitBgTaskNotify()
}

// codexPersistBatchLimit caps one persist-batch window.
const codexPersistBatchLimit = 32

// flushCodexBatch parses a batch of notifications and dispatches their events
// inside ONE persist-batch transaction (when the session has a db wired).
// Terminal notifications end the batch early: their result-side writes (cost,
// stats) must commit with the batch, not after it.
func (s *WorkspaceSession) flushCodexBatch(ctx context.Context, app *codexAppServerClient, cliAdapter adapter.Adapter, batch []codexAppServerNotification) {
	if len(batch) == 0 {
		return
	}
	var all []adapter.ParsedEvent
	for _, notif := range batch {
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
		events = s.enrichCodexErrors(events, app)
		all = append(all, events...)
	}
	if len(all) == 0 {
		return
	}
	// NOTE: no transaction window here. Wrapping handleEvent's heterogeneous
	// writes (agent messages, session columns, tasks, stats — only SOME via
	// writeQ) in an external tx deadlocks SQLite's single connection (the tx
	// holds the only conn while non-tx writes inside handleEvent wait for it)
	// and splits writes across tx/non-tx on PostgreSQL. Batching for write
	// throughput must live INSIDE persistEvent, never around handleEvent.
	s.dispatchCodexEvents(ctx, all)
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

// enrichCodexErrors fills in empty codex error messages with actionable context.
// codex emits `error` notifications with an absent message on network failures
// (e.g. it cannot reach chatgpt.com), which used to surface as bare "Error:"
// bubbles. The app-server's stderr carries the real reason (model refresh
// timeout / HTTP request failure) — attach its tail; when stderr has nothing
// yet, fall back to a connectivity hint.
func (s *WorkspaceSession) enrichCodexErrors(events []adapter.ParsedEvent, app *codexAppServerClient) []adapter.ParsedEvent {
	for i, ev := range events {
		if !ev.IsError || strings.TrimSpace(ev.Result) != "" {
			continue
		}
		if tail := strings.TrimSpace(app.StderrTail(400)); tail != "" {
			events[i].Result = "Codex error, cause visible in stderr: " + truncate(tail, 400)
			continue
		}
		events[i].Result = "Codex returned an empty error, usually because it cannot connect to the model service (e.g. chatgpt.com). Please check network connectivity or proxy settings"
	}
	return events
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
	openaiBase := ""
	for _, e := range wsEnvVars {
		switch {
		case e.Key == "NIUNIU_MODEL" && e.Value != "":
			out.model = e.Value
			s.mu.Lock()
			s.modelName = e.Value
			s.mu.Unlock()
		case e.Key == "OPENAI_BASE_URL":
			openaiBase = e.Value
		}
	}
	// provider 中转协议适配：codex 内置 provider 默认走 Responses API
	// （{base}/responses），而订阅平台中转（智谱/DeepSeek 等）只提供 Chat
	// Completions——OPENAI_BASE_URL 环境变量只换 URL 不换协议，必须以
	// model_providers 覆盖声明 wire_api="chat" 并切换 model_provider，
	// 否则中转返回 404、codex 发出空 error 通知。
	out.configArgs = codexProviderConfigArgs(openaiBase)

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
