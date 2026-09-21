package agentproxy

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/agentbackend"
	cursorbackend "github.com/niuniu-dev/niuniu/internal/agentbackend/cursor"
	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/sceneenv"
)

// runCursorBackendTurn drives one cursor workspace turn through
// agentbackend/cursor (ACP over `agent acp`), mapping neutral events onto
// niuniu's proxy-chat model and bridging session/request_permission to the
// permission gate.
//
// Why ACP rather than the headless `cursor-agent -p` path this integration
// originally used: ACP is a documented first-class mode of Cursor's CLI
// (JSON-RPC 2.0 over stdio, the same protocol family as goose) and it is the
// only surface that gives niuniu a real permission gate, multi-turn on one
// process, cooperative cancel, and explicitly-passed MCP servers. The `-p` path
// has a reported defect where MCP tools are not injected at all, which would
// have left cursor workspaces unable to call niuniu-mcp (i.e. unable to move
// their own kanban card).
//
// Event/cost handling is shared with the goose path (handleGooseEvent /
// recordGooseCost / signalGooseTurnDone): both speak the same neutral
// agentbackend.Event, so duplicating that mapping would be pure copy-paste.
func (s *WorkspaceSession) runCursorBackendTurn(ctx context.Context, workDir, content, msgId string) error {
	be, err := s.getOrStartCursorBackend(ctx, workDir)
	if err != nil {
		slog.Error("cursor: backend start failed", "workspaceID", s.workspaceID, "workDir", workDir, "err", err)
		errEv := NewOutputEvent(EventError, "Failed to start cursor agent: "+err.Error(), msgId, "assistant", s.workspaceID)
		s.hub.Broadcast(s.workspaceID, errEv)
		s.signalGooseTurnDone(ctx, msgId, true, err.Error())
		return err
	}

	// Bound a silently-wedged process the same way every other engine does.
	window := s.turnInactivityTimeout
	if window <= 0 {
		window = defaultTurnInactivityTimeout
	}
	// INACTIVITY watchdog (not a hard turn timeout): a hard 15-min cap killed
	// legitimate long turns (a 30-min build never finishes). Same contract as
	// every other engine: cancel only when the backend has produced no output
	// for the window AND no tool has been in flight past its grace ceiling.
	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go watchBackendTurnInactivity(s, cancel, turnCtx.Done(), window)

	ch, err := be.Prompt(turnCtx, agentbackend.PromptRequest{Message: content})
	if err != nil {
		slog.Error("cursor: prompt failed", "workspaceID", s.workspaceID, "err", err)
		s.signalGooseTurnDone(ctx, msgId, true, err.Error())
		return err
	}

	var lastErr string
	for ev := range ch {
		s.mu.Lock()
		s.lastActivityAt = time.Now() // steady output resets the watchdog clock
		s.mu.Unlock()
		s.handleGooseEvent(ctx, ev, msgId)
		if ev.Type == agentbackend.EventError {
			lastErr = ev.Error
		}
	}
	s.signalGooseTurnDone(ctx, msgId, lastErr != "", lastErr)
	return nil
}

// getOrStartCursorBackend lazily creates and starts the session's cursor
// backend, capturing the ACP session id so the SPA shows a live session.
func (s *WorkspaceSession) getOrStartCursorBackend(ctx context.Context, workDir string) (agentbackend.Backend, error) {
	s.mu.Lock()
	if s.cursorBackend != nil {
		be := s.cursorBackend
		s.mu.Unlock()
		return be, nil
	}
	s.mu.Unlock()

	// Resolve workspace env (provider keys, model, NIUNIU_* controls). Cursor
	// authenticates through its own login or CURSOR_API_KEY / CURSOR_AUTH_TOKEN,
	// so provider vars pass through untranslated — unlike goose, which needs the
	// GOOSE_PROVIDER__* triad.
	var envSlice []string
	var model, permissionMode string
	envVars, envErr := sceneenv.Resolve(ctx, s.q, s.workspaceID)
	if envErr != nil {
		slog.Warn("cursor: resolve workspace env failed", "workspaceID", s.workspaceID, "err", envErr)
	} else {
		// Mirror the claude path in ensureProcess: record the provider this
		// spawn resolved, so non-claude workspaces get the same pill/state
		// tracking.
		s.recordActiveProvider(ctx)
	}
	for _, e := range envVars {
		// NIUNIU_* are internal control keys — never leak them to the child
		// (consistent with injectCLIEnv in adapter/spawn.go).
		if strings.HasPrefix(e.Key, "NIUNIU_") {
			switch e.Key {
			case "NIUNIU_MODEL":
				if e.Value != "" {
					model = e.Value
				}
			case "NIUNIU_PERMISSION_MODE":
				permissionMode = e.Value
			}
			continue
		}
		envSlice = append(envSlice, e.Key+"="+e.Value)
	}
	if model != "" {
		s.mu.Lock()
		s.modelName = model
		s.mu.Unlock()
	}

	opts := cursorbackend.Options{
		Command:           s.cfg.Agent.CursorCli.Command, // default "agent"
		Args:              s.cfg.Agent.CursorCli.Args,
		WorkDir:           workDir,
		Env:               envSlice,
		Model:             model,
		Mode:              cursorACPMode(permissionMode),
		ResolvePermission: s.cursorResolvePermission,
	}

	// MCP collaboration: cursor consumes niuniu-mcp (boards / data / memory /
	// documents) as an MCP client, handed over at session/new. Best-effort — a
	// missing niuniu-mcp binary leaves the agent running without niuniu's tools.
	if s.mcpWriter != nil {
		projectID, _ := s.q.GetProjectIDForWorkspace(ctx, s.workspaceID)
		entry, err := s.mcpWriter.NiuniuMcpServer(config.MCPGenerateOptions{
			ProjectID:    projectID,
			WorkspaceID:  s.workspaceID,
			InboxDir:     filepath.Join(workDir, ".team", "inboxes"),
			SessionToken: s.sessionToken,
		})
		if err != nil {
			slog.Warn("cursor: niuniu-mcp server resolution failed (running without niuniu MCP)",
				"workspaceID", s.workspaceID, "err", err)
		} else {
			opts.McpServers = []cursorbackend.McpServer{{
				Name:    entry.Name,
				Command: entry.Command,
				Args:    entry.Args,
				Env:     entry.Env,
			}}
		}
	}

	impl := cursorbackend.New(opts)
	var be agentbackend.Backend = impl
	if err := be.Start(ctx); err != nil {
		return nil, err
	}

	// Surface the ACP session id through the normal session-capture path so the
	// SPA and DB agree a session is live (the one-shot engines get this from a
	// system/init stream line; ACP returns it from session/new instead).
	if sid := impl.SessionID(); sid != "" {
		s.handleEvent(ctx, ParsedEvent{Type: "system", Subtype: "init", SessionID: sid}, s.turnMsgId)
	}

	s.mu.Lock()
	if s.cursorBackend == nil {
		s.cursorBackend = be
	}
	be = s.cursorBackend
	s.mu.Unlock()
	return be, nil
}

// cursorACPMode maps niuniu's permission mode onto an ACP session mode. Cursor
// supports "agent" (full tools), "plan" and "ask" (both read-only).
//
// Only "plan" is mapped to a restricted mode; every other niuniu mode keeps full
// tool access and relies on the permission gate for per-call approval, which is
// exactly the capability ACP adds over the old `--force` behavior.
func cursorACPMode(permissionMode string) string {
	if permissionMode == "plan" {
		return "plan"
	}
	return ""
}

// cursorResolvePermission bridges ACP session/request_permission to niuniu's
// permission gate (the same SPA approval card codex and goose use). Nil gate →
// fail closed (reject).
//
// autohost/bypassPermissions auto-approve: an unattended run has nobody to
// answer the card, and blocking there would wedge the turn until the watchdog
// fires. Interactive modes go to the user.
func (s *WorkspaceSession) cursorResolvePermission(ctx context.Context, req agentbackend.PermissionRequest) (agentbackend.PermissionDecision, error) {
	if s.autoApprovePermissions(ctx) {
		return agentbackend.PermissionDecision{Confirmed: true}, nil
	}
	if s.permissionGate == nil {
		return agentbackend.PermissionDecision{Cancelled: true}, nil
	}
	input := map[string]any{
		"method":  req.Method,
		"title":   req.Title,
		"message": req.Message,
		"options": req.Options,
	}
	behavior, err := s.permissionGate.Request(
		ctx, s.workspaceID, s.ownerType, s.ownerID, s.sessionId,
		"cursor:acp_permission", input,
	)
	if err != nil {
		return agentbackend.PermissionDecision{Cancelled: true}, err
	}
	if behavior == "allow" {
		return agentbackend.PermissionDecision{Confirmed: true}, nil
	}
	return agentbackend.PermissionDecision{Cancelled: true}, nil
}

// autoApprovePermissions reports whether the workspace runs in a mode with no
// human available to answer an approval card (autohost / bypassPermissions).
func (s *WorkspaceSession) autoApprovePermissions(ctx context.Context) bool {
	envVars, err := sceneenv.Resolve(ctx, s.q, s.workspaceID)
	if err != nil {
		// Unknown mode: prefer auto-approve over wedging an unattended turn. The
		// outer workspace isolation is the real sandbox.
		return true
	}
	for _, e := range envVars {
		if e.Key == "NIUNIU_PERMISSION_MODE" {
			switch e.Value {
			case "autohost", "bypassPermissions":
				return true
			default:
				return false
			}
		}
	}
	// No mode configured: workspaces default to bypassPermissions at creation.
	return true
}
