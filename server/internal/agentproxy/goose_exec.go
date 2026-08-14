package agentproxy

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/agentbackend"
	"github.com/niuniu-dev/niuniu/internal/agentbackend/goose"
	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/sceneenv"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// runGooseBackendTurn drives one goose workspace turn through the reusable
// agentbackend.goose.Backend (ACP over `goose acp`), mapping its neutral events
// onto niuniu's proxy-chat model (messages / cost / SSE) and bridging
// session/request_permission frames to the permission gate. It is the "frame →
// proxy chat" adaptation edge for the goose integration.
func (s *WorkspaceSession) runGooseBackendTurn(ctx context.Context, workDir, content, msgId string) error {
	be, err := s.getOrStartGooseBackend(ctx, workDir)
	if err != nil {
		slog.Error("goose: backend start failed", "workspaceID", s.workspaceID, "workDir", workDir, "err", err)
		errEv := NewOutputEvent(EventError, "Failed to start goose agent: "+err.Error(), msgId, "assistant", s.workspaceID)
		s.hub.Broadcast(s.workspaceID, errEv)
		s.signalGooseTurnDone(ctx, msgId, true, err.Error())
		return err
	}

	// Bound a silently-wedged goose process the same way the Claude path does.
	window := s.turnInactivityTimeout
	if window <= 0 {
		window = defaultTurnInactivityTimeout
	}
	turnCtx, cancel := context.WithTimeout(ctx, window)
	defer cancel()

	ch, err := be.Prompt(turnCtx, agentbackend.PromptRequest{Message: content})
	if err != nil {
		slog.Error("goose: prompt failed", "workspaceID", s.workspaceID, "err", err)
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

// getOrStartGooseBackend lazily creates and starts the session's goose backend.
func (s *WorkspaceSession) getOrStartGooseBackend(ctx context.Context, workDir string) (agentbackend.Backend, error) {
	s.mu.Lock()
	if s.gooseBackend != nil {
		be := s.gooseBackend
		s.mu.Unlock()
		return be, nil
	}
	s.mu.Unlock()

	// Resolve workspace env vars (provider API keys, model, NIUNIU_* controls)
	// and pass them to the goose backend so it inherits the user's configured
	// provider / account credentials. Best-effort: a resolve failure leaves
	// the backend running with its own config defaults.
	//
	// goose does NOT read generic OPENAI_BASE_URL/OPENAI_API_KEY for custom
	// OpenAI-compatible endpoints — it needs the GOOSE_PROVIDER__* triad
	// (TYPE/HOST/API_KEY). We translate the provider expansion's OPENAI_* vars
	// into goose's native vars here. See:
	//   https://goose-docs.ai/docs/guides/environment-variables
	var envSlice []string
	var model string
	hasCustomEndpoint := false
	envVars, envErr := sceneenv.Resolve(ctx, s.q, s.workspaceID)
	if envErr != nil {
		slog.Warn("goose: resolve workspace env failed", "workspaceID", s.workspaceID, "err", envErr)
	}
	for _, e := range envVars {
		// NIUNIU_* are niuniu-internal control keys — never leak them to the
		// agent process (consistent with injectCLIEnv in adapter/spawn.go).
		if strings.HasPrefix(e.Key, "NIUNIU_") {
			if e.Key == "NIUNIU_MODEL" && e.Value != "" {
				model = e.Value
			}
			continue
		}
		switch e.Key {
		case "OPENAI_BASE_URL":
			envSlice = append(envSlice, "GOOSE_PROVIDER__TYPE=openai")
			envSlice = append(envSlice, "GOOSE_PROVIDER__HOST="+e.Value)
			hasCustomEndpoint = true
		case "OPENAI_API_KEY":
			envSlice = append(envSlice, "GOOSE_PROVIDER__API_KEY="+e.Value)
		case "OPENAI_MODEL":
			if model == "" {
				model = e.Value
			}
			// goose reads GOOSE_MODEL, set below via opts.Model → buildEnv
		default:
			envSlice = append(envSlice, e.Key+"="+e.Value)
		}
	}
	if hasCustomEndpoint {
		envSlice = append(envSlice, "GOOSE_PROVIDER=openai")
	}

	opts := goose.Options{
		Command: s.cfg.Agent.GooseCli.Command, // default "goose"
		Args:    s.cfg.Agent.GooseCli.Args,
		WorkDir: workDir,
		Env:     envSlice,
		Model:   model,
		// Provider/Model are workspace-level; goose falls back to its own config
		// (OpenRouter/Ollama/compatible endpoints for domestic models).
		ResolvePermission: s.gooseResolvePermission,
	}
	// MCP collaboration: goose consumes niuniu-mcp (boards / data / memory /
	// documents) as an MCP client. Best-effort — a missing niuniu-mcp binary
	// leaves the agent running without the niuniu MCP surface.
	if s.mcpWriter != nil {
		projectID, _ := s.q.GetProjectIDForWorkspace(ctx, s.workspaceID)
		entry, err := s.mcpWriter.NiuniuMcpServer(config.MCPGenerateOptions{
			ProjectID:    projectID,
			WorkspaceID:  s.workspaceID,
			InboxDir:     filepath.Join(workDir, ".team", "inboxes"),
			SessionToken: s.sessionToken,
		})
		if err != nil {
			slog.Warn("goose: niuniu-mcp server resolution failed (running without niuniu MCP)",
				"workspaceID", s.workspaceID, "err", err)
		} else {
			opts.McpServers = []goose.McpServer{{
				Name:    entry.Name,
				Command: entry.Command,
				Args:    entry.Args,
				Env:     entry.Env,
			}}
		}
	}
	var be agentbackend.Backend = goose.New(opts)
	if err := be.Start(ctx); err != nil {
		return nil, err
	}

	s.mu.Lock()
	if s.gooseBackend == nil {
		s.gooseBackend = be
	}
	be = s.gooseBackend
	s.mu.Unlock()
	return be, nil
}

// gooseResolvePermission bridges ACP session/request_permission frames to
// niuniu's permission gate (the same SPA approval card / SSE flow codex uses).
// Nil gate → fail closed (deny). Only the confirm/allow behavior is mapped;
// value-based methods surface as confirm until a richer host bridge lands.
func (s *WorkspaceSession) gooseResolvePermission(ctx context.Context, req agentbackend.PermissionRequest) (agentbackend.PermissionDecision, error) {
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
		"goose:acp_permission", input,
	)
	if err != nil {
		return agentbackend.PermissionDecision{Cancelled: true}, err
	}
	if behavior == "allow" {
		return agentbackend.PermissionDecision{Confirmed: true}, nil
	}
	return agentbackend.PermissionDecision{Cancelled: true}, nil
}

// handleGooseEvent maps one neutral agentbackend.Event onto niuniu's OutputEvent
// model and persists + broadcasts it.
func (s *WorkspaceSession) handleGooseEvent(ctx context.Context, ev agentbackend.Event, msgId string) {
	switch ev.Type {
	case agentbackend.EventText:
		s.persistAndBroadcast(ctx, NewOutputEvent(EventText, ev.Text, msgId, "assistant", s.workspaceID), 0)
	case agentbackend.EventThinking:
		s.persistAndBroadcast(ctx, NewOutputEvent(EventThinking, ev.Thinking, msgId, "assistant", s.workspaceID), 0)
	case agentbackend.EventToolUse:
		out := NewOutputEvent(EventToolUse, "", msgId, "assistant", s.workspaceID)
		out.ToolName = ev.ToolName
		out.ToolInput = ev.ToolInput
		out.ToolUseId = ev.ToolUseID
		s.persistAndBroadcast(ctx, out, 0)
	case agentbackend.EventToolResult:
		out := NewOutputEvent(EventToolResult, ev.Text, msgId, "user", s.workspaceID)
		out.ToolUseId = ev.ToolUseID
		out.IsError = ev.IsError
		s.persistAndBroadcast(ctx, out, 0)
	case agentbackend.EventDone:
		done := NewOutputEvent(EventDone, "", msgId, "assistant", s.workspaceID)
		done.CostUsd = ev.CostUSD
		done.NumTurns = ev.NumTurns
		done.DurationMs = ev.DurationMs
		done.InputTokens = ev.InputTokens
		done.OutputTokens = ev.OutputTokens
		// Feed the context-window occupancy (drives the usage pill + any future
		// compaction heuristic) the same way the Claude path does.
		if ev.InputTokens+ev.CacheReadTokens > 0 {
			s.mu.Lock()
			s.lastContextTokens = ev.InputTokens + ev.CacheReadTokens
			s.mu.Unlock()
		}
		s.persistAndBroadcast(ctx, done, 0)
		s.recordGooseCost(ctx, ev)
	case agentbackend.EventError:
		s.persistAndBroadcast(ctx, NewOutputEvent(EventError, ev.Error, msgId, "assistant", s.workspaceID), 0)
	}
}

// recordGooseCost persists the turn's cost + token usage (mirrors the Claude
// result path's accounting for goose's usage telemetry).
func (s *WorkspaceSession) recordGooseCost(ctx context.Context, ev agentbackend.Event) {
	if s.isTemporary {
		return
	}
	activeRunID := s.ActiveRunID()
	s.q.CreateWorkspaceCost(ctx, store.CreateWorkspaceCostParams{
		WorkspaceID:  s.workspaceID,
		SessionID:    sql.NullString{String: s.sessionId, Valid: s.sessionId != ""},
		MessageID:    sql.NullString{String: s.turnMsgId, Valid: true},
		CostUsd:      ev.CostUSD,
		NumTurns:     int64(ev.NumTurns),
		DurationMs:   ev.DurationMs,
		HarnessRunID: sql.NullInt64{Int64: activeRunID, Valid: activeRunID != 0},
	})
	if err := s.q.UpsertWorkspaceStatsAI(ctx, store.UpsertWorkspaceStatsAIParams{
		WorkspaceID:     s.workspaceID,
		OwnerType:       s.ownerType,
		OwnerID:         s.ownerID,
		TotalTurns:      int64(ev.NumTurns),
		TotalDurationMs: ev.DurationMs,
		InputTokens:     int64(ev.InputTokens),
		OutputTokens:    int64(ev.OutputTokens),
	}); err != nil {
		slog.Warn("goose: UpsertWorkspaceStatsAI failed", "workspaceID", s.workspaceID, "error", err)
	}
}

// signalGooseTurnDone marks the turn complete: updates the session columns to
// idle, sets per-turn error state, and signals SendLoop's turnDone so the
// workspace can transition to idle / attention.
func (s *WorkspaceSession) signalGooseTurnDone(ctx context.Context, msgId string, isErr bool, result string) {
	s.q.UpdateSessionColumns(ctx, store.UpdateSessionColumnsParams{
		SessionID:     sql.NullString{String: s.sessionId, Valid: s.sessionId != ""},
		SessionStatus: sql.NullString{String: string(StatusIdle), Valid: true},
		ID:            s.workspaceID,
	})
	s.mu.Lock()
	s.lastTurnError = isErr
	s.lastTurnResult = result
	ch := s.turnDone
	s.mu.Unlock()
	s.emitBgTaskNotify()
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}