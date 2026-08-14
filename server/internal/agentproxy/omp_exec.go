package agentproxy

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
	//
	// omp does NOT read generic OPENAI_BASE_URL/OPENAI_API_KEY for custom
	// OpenAI-compatible endpoints, nor OMP_MODEL for model selection. Custom
	// endpoints require a models.yml provider definition (loaded via
	// PI_CODING_AGENT_DIR), and model selection requires the --model CLI flag.
	// See: https://github.com/can1357/oh-my-pi/blob/main/docs/environment-variables.md
	var envSlice []string
	var model string
	openaiBaseURL, openaiAPIKey := "", ""
	envVars, envErr := sceneenv.Resolve(ctx, s.q, s.workspaceID)
	if envErr != nil {
		slog.Warn("omp: resolve workspace env failed", "workspaceID", s.workspaceID, "err", envErr)
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
		// Capture the OpenAI-protocol endpoint to generate a models.yml custom
		// provider (omp can't consume these as raw env vars).
		switch e.Key {
		case "OPENAI_BASE_URL":
			openaiBaseURL = e.Value
		case "OPENAI_API_KEY":
			openaiAPIKey = e.Value
		case "OPENAI_MODEL":
			if model == "" {
				model = e.Value
			}
		default:
			envSlice = append(envSlice, e.Key+"="+e.Value)
		}
	}

	// If the workspace has a custom OpenAI-compatible endpoint, materialize it
	// as an omp models.yml provider definition + select the model via --model.
	// PI_CODING_AGENT_DIR scopes this to the workspace so it never clobbers the
	// user's global ~/.omp/agent config.
	var args []string
	args = append(args, s.cfg.Agent.OmpCli.Args...)
	if openaiBaseURL != "" && openaiAPIKey != "" {
		agentDir := filepath.Join(workDir, ".omp", "agent")
		if err := os.MkdirAll(agentDir, 0o755); err != nil {
			slog.Warn("omp: create agent dir failed", "dir", agentDir, "err", err)
		} else if writeOMPModelsYML(agentDir, openaiBaseURL, openaiAPIKey, model) {
			envSlice = append(envSlice, "PI_CODING_AGENT_DIR="+agentDir)
			if model != "" {
				args = append(args, "--model", model)
			}
		}
	}

	var be agentbackend.Backend = omp.New(omp.Options{
		Command: s.cfg.Agent.OmpCli.Command, // default "omp"
		Args:    args,
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
		// Feed the context-window occupancy (drives the usage pill + any future
		// compaction heuristic) the same way the Claude path does.
		if ev.InputTokens+ev.CacheReadTokens > 0 {
			s.mu.Lock()
			s.lastContextTokens = ev.InputTokens + ev.CacheReadTokens
			s.mu.Unlock()
		}
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

// writeOMPModelsYML materializes a custom OpenAI-compatible provider as an omp
// models.yml in the given agent dir. omp reads models.yml from PI_CODING_AGENT_DIR
// (set by the caller). The provider is a single OpenAI Chat Completions endpoint
// with the API key inline (auth: apiKey), so omp needs no separate OAuth/login.
// Model id falls back to "default" so omp always has a selectable model.
//
// Schema: https://github.com/can1357/oh-my-pi/blob/main/packages/coding-agent/src/config/models-config-schema-bundle.ts
func writeOMPModelsYML(agentDir, baseURL, apiKey, model string) bool {
	if model == "" {
		model = "default"
	}
	content := fmt.Sprintf(`providers:
  niuniu:
    baseUrl: %q
    apiKey: %q
    api: openai-completions
    auth: apiKey
    models:
      - id: %q
        name: %q
`, baseURL, apiKey, model, model)
	path := filepath.Join(agentDir, "models.yml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		slog.Warn("omp: write models.yml failed", "path", path, "err", err)
		return false
	}
	return true
}