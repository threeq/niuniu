package agentproxy

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/niuniu-dev/niuniu/internal/agentbackend"
	"github.com/niuniu-dev/niuniu/internal/agentbackend/omp"
	"github.com/niuniu-dev/niuniu/internal/sceneenv"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// runOMPBackendTurn drives one omp workspace turn through the reusable
// agentbackend.omp.Backend (RPC over `omp --mode rpc`), mapping its neutral
// events onto niuniu's proxy-chat model (messages / cost / SSE) and bridging
// extension_ui_request frames to the permission gate. It is the "frame → proxy
// chat" adaptation edge for the omp integration.
func (s *WorkspaceSession) runOMPBackendTurn(ctx context.Context, workDir, content, msgId string) error {
	be, err := s.getOrStartOMPBackend(ctx, workDir)
	if err != nil {
		slog.Error("omp: backend start failed", "workspaceID", s.workspaceID, "workDir", workDir, "err", err)
		errEv := NewOutputEvent(EventError, "Failed to start omp agent: "+err.Error(), msgId, "assistant", s.workspaceID)
		s.hub.Broadcast(s.workspaceID, errEv)
		s.signalOMPTurnDone(ctx, msgId, true, err.Error())
		return err
	}

	// Bound a silently-wedged omp process the same way the Claude path does.
	window := s.turnInactivityTimeout
	if window <= 0 {
		window = defaultTurnInactivityTimeout
	}
	turnCtx, cancel := context.WithTimeout(ctx, window)
	defer cancel()

	ch, err := be.Prompt(turnCtx, agentbackend.PromptRequest{Message: content})
	if err != nil {
		slog.Error("omp: prompt failed", "workspaceID", s.workspaceID, "err", err)
		s.signalOMPTurnDone(ctx, msgId, true, err.Error())
		return err
	}

	var lastErr string
	for ev := range ch {
		s.mu.Lock()
		s.lastActivityAt = time.Now() // steady output resets the watchdog clock
		s.mu.Unlock()
		s.handleOMPEvent(ctx, ev, msgId)
		if ev.Type == agentbackend.EventError {
			lastErr = ev.Error
		}
	}
	s.signalOMPTurnDone(ctx, msgId, lastErr != "", lastErr)
	return nil
}

// getOrStartOMPBackend lazily creates and starts the session's omp backend.
func (s *WorkspaceSession) getOrStartOMPBackend(ctx context.Context, workDir string) (agentbackend.Backend, error) {
	s.mu.Lock()
	if s.ompBackend != nil {
		be := s.ompBackend
		s.mu.Unlock()
		return be, nil
	}
	s.mu.Unlock()

	// Resolve workspace env vars (provider API keys, model, NIUNIU_* controls)
	// and pass them to the omp backend so it inherits the user's configured
	// provider / account credentials. Best-effort: a resolve failure leaves
	// the backend running with its own config defaults.
	var envSlice []string
	var model string
	if envVars, envErr := sceneenv.Resolve(ctx, s.q, s.workspaceID); envErr != nil {
		slog.Warn("omp: resolve workspace env failed", "workspaceID", s.workspaceID, "err", envErr)
	} else {
		for _, e := range envVars {
			envSlice = append(envSlice, e.Key+"="+e.Value)
			if e.Key == "NIUNIU_MODEL" && e.Value != "" {
				model = e.Value
			}
		}
	}

	var be agentbackend.Backend = omp.New(omp.Options{
		Command: s.cfg.Agent.OmpCli.Command, // default "omp"
		Args:    s.cfg.Agent.OmpCli.Args,
		WorkDir: workDir,
		Env:     envSlice,
		Model:   model,
		// Provider/Model are workspace-level; omp falls back to its own config.
		ResolvePermission: s.ompResolvePermission,
	})
	if err := be.Start(ctx); err != nil {
		return nil, err
	}

	s.mu.Lock()
	if s.ompBackend == nil {
		s.ompBackend = be
	}
	be = s.ompBackend
	s.mu.Unlock()
	return be, nil
}

// ompResolvePermission bridges omp extension_ui_request frames to niuniu's
// permission gate (the same SPA approval card / SSE flow codex uses). Nil gate
// → fail closed (cancel). Only the confirm/allow behavior is mapped; value-based
// methods (input/select) surface as confirm until a richer host bridge lands.
func (s *WorkspaceSession) ompResolvePermission(ctx context.Context, req agentbackend.PermissionRequest) (agentbackend.PermissionDecision, error) {
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
		"omp:extension_ui", input,
	)
	if err != nil {
		return agentbackend.PermissionDecision{Cancelled: true}, err
	}
	if behavior == "allow" {
		return agentbackend.PermissionDecision{Confirmed: true}, nil
	}
	return agentbackend.PermissionDecision{Cancelled: true}, nil
}

// handleOMPEvent maps one neutral agentbackend.Event onto niuniu's OutputEvent
// model and persists + broadcasts it.
func (s *WorkspaceSession) handleOMPEvent(ctx context.Context, ev agentbackend.Event, msgId string) {
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
		s.persistAndBroadcast(ctx, done, 0)
		s.recordOMPCost(ctx, ev)
	case agentbackend.EventError:
		s.persistAndBroadcast(ctx, NewOutputEvent(EventError, ev.Error, msgId, "assistant", s.workspaceID), 0)
	}
}

// recordOMPCost persists the turn's cost + token usage (mirrors the Claude
// result path's accounting for omp's agent_end telemetry).
func (s *WorkspaceSession) recordOMPCost(ctx context.Context, ev agentbackend.Event) {
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
		slog.Warn("omp: UpsertWorkspaceStatsAI failed", "workspaceID", s.workspaceID, "error", err)
	}
}

// signalOMPTurnDone marks the turn complete: updates the session columns to
// idle, sets per-turn error state, and signals SendLoop's turnDone so the
// workspace can transition to idle / attention.
func (s *WorkspaceSession) signalOMPTurnDone(ctx context.Context, msgId string, isErr bool, result string) {
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