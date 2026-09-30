package agentproxy

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/agentbackend"
	"github.com/niuniu-dev/niuniu/internal/agentbackend/niuniuagent"
	"github.com/niuniu-dev/niuniu/internal/sceneenv"
)

// runNiuniuAgentBackendTurn drives one niuniu-agent workspace turn through the
// reusable agentbackend.niuniuagent.Backend (ACP over `niuniu-agent acp`),
// mapping its neutral events onto niuniu's proxy-chat model and bridging
// session/request_permission requests to the permission gate. This is
// niuniu's own engine — the same binary the agent/ module ships.
func (s *WorkspaceSession) runNiuniuAgentBackendTurn(ctx context.Context, workDir, content, msgId string) error {
	s.markAgentRunning(ctx)
	s.recordActiveProvider(ctx)
	be, err := s.getOrStartNiuniuAgentBackend(ctx, workDir)
	if err != nil {
		slog.Error("niuniu-agent: backend start failed", "workspaceID", s.workspaceID, "workDir", workDir, "err", err)
		errEv := NewOutputEvent(EventError, "Failed to start niuniu-agent: "+err.Error(), msgId, "assistant", s.workspaceID)
		s.hub.Broadcast(s.workspaceID, errEv)
		s.signalGooseTurnDone(ctx, msgId, true, err.Error())
		return err
	}

	// Bound a silently-wedged process the same way the other engines do.
	window := s.turnInactivityTimeout
	if window <= 0 {
		window = defaultTurnInactivityTimeout
	}
	turnCtx, cancel := context.WithTimeout(ctx, window)
	defer cancel()

	ch, err := be.Prompt(turnCtx, agentbackend.PromptRequest{Message: content})
	if err != nil {
		slog.Error("niuniu-agent: prompt failed", "workspaceID", s.workspaceID, "err", err)
		s.signalGooseTurnDone(ctx, msgId, true, err.Error())
		return err
	}

	var lastErr string
	for ev := range ch {
		s.mu.Lock()
		s.lastActivityAt = time.Now()
		s.mu.Unlock()
		s.handleNiuniuEvent(ctx, ev, msgId) // delta-aware mapping: stream broadcast + aggregated persist
		if ev.Type == agentbackend.EventError {
			lastErr = ev.Error
		}
	}
	s.signalGooseTurnDone(ctx, msgId, lastErr != "", lastErr)
	return nil
}

// getOrStartNiuniuAgentBackend lazily creates and starts the session's
// niuniu-agent backend. The agent reads ANTHROPIC_* / OPENAI_* /
// NIUNIU_AGENT_PROVIDER from its environment (it speaks both protocol
// families natively), so the workspace env passes through with only the
// NIUNIU_* control keys stripped.
func (s *WorkspaceSession) getOrStartNiuniuAgentBackend(ctx context.Context, workDir string) (agentbackend.Backend, error) {
	// Capability injection (P4): project inject.md (niuniu-mcp tool family,
	// AUTOHOST_DONE convention, kanban discipline) and regenerate .mcp.json
	// (niuniu-mcp) so the agent's own MCP client mounts the niuniu tools —
	// the same generated config the claude engine consumes. Best-effort: a
	// failed projection logs and the session proceeds standalone.
	projectID, perr := s.q.GetProjectIDForWorkspace(ctx, s.workspaceID)
	if perr != nil {
		slog.Warn("niuniu-agent: resolve project for projection failed", "workspaceID", s.workspaceID, "err", perr)
	}
	if err := projectNiuniuAgentFiles(workDir, s.sessionToken, s.mcpWriter, projectID, s.workspaceID,
		filepath.Join(workDir, ".team", "inboxes")); err != nil {
		slog.Warn("niuniu-agent: capability projection failed (continuing standalone)", "workspaceID", s.workspaceID, "err", err)
	}

	s.mu.Lock()
	if s.niuniuAgentBackend != nil {
		be := s.niuniuAgentBackend
		s.mu.Unlock()
		return be, nil
	}
	s.mu.Unlock()

	var envSlice []string
	var model string
	var openaiEndpoint bool
	envVars, envErr := sceneenv.Resolve(ctx, s.q, s.workspaceID)
	if envErr != nil {
		slog.Warn("niuniu-agent: resolve workspace env failed", "workspaceID", s.workspaceID, "err", envErr)
	}
	for _, e := range envVars {
		if strings.HasPrefix(e.Key, "NIUNIU_") {
			if e.Key == "NIUNIU_MODEL" && e.Value != "" {
				model = e.Value
			}
			// Agent capability control keys are the exception to the strip
			// rule: they tune the agent's reasoning/search surface, so a
			// workspace-level binding must reach the agent process.
			switch e.Key {
			case "NIUNIU_AGENT_THINKING", "NIUNIU_AGENT_SEARCH",
				"NIUNIU_AGENT_MODEL_HIGH", "NIUNIU_AGENT_MODEL_FAST":
				if e.Value != "" {
					envSlice = append(envSlice, e.Key+"="+e.Value)
				}
			}
			continue
		}
		switch e.Key {
		case "OPENAI_BASE_URL":
			openaiEndpoint = true
		case "OPENAI_MODEL":
			if model == "" {
				model = e.Value
			}
		}
		envSlice = append(envSlice, e.Key+"="+e.Value)
	}
	if openaiEndpoint {
		envSlice = append(envSlice, "NIUNIU_AGENT_PROVIDER=openai")
	}
	// Local single-user host: default the agent's ACP approval flow to bypass
	// (same contract as claude's bypassPermissions) so tool calls do not park
	// on an approval card the desktop UI may not render. Workspace env can
	// override with NIUNIU_AGENT_PERMISSION_MODE=normal.
	envSlice = append(envSlice, "NIUNIU_AGENT_PERMISSION_MODE=bypass")
	if model != "" {
		// niuniu's workspace-level model selection lands on whichever protocol
		// family the endpoint speaks; decorated "[...]" context-tier suffixes
		// are an upstream harness convention the API rejects, so strip them.
		if i := strings.IndexByte(model, '['); i > 0 {
			model = model[:i]
		}
		if openaiEndpoint {
			envSlice = append(envSlice, "OPENAI_MODEL="+model)
		} else {
			envSlice = append(envSlice, "ANTHROPIC_MODEL="+model)
		}
	}

	opts := niuniuagent.Options{
		Command:           resolveNiuniuAgentCommand(s.cfg.Agent.NiuniuAgentCli.Command),
		Args:              s.cfg.Agent.NiuniuAgentCli.Args,
		WorkDir:           workDir,
		Env:               envSlice,
		ResolvePermission: s.niuniuAgentResolvePermission,
	}
	var be agentbackend.Backend = niuniuagent.New(opts)
	if err := be.Start(ctx); err != nil {
		return nil, err
	}

	s.mu.Lock()
	if s.niuniuAgentBackend == nil {
		s.niuniuAgentBackend = be
	}
	be = s.niuniuAgentBackend
	s.mu.Unlock()
	return be, nil
}

// niuniuAgentResolvePermission bridges the agent's session/request_permission
// requests to niuniu's permission gate (same SPA approval card flow). Nil gate
// → fail closed.
func (s *WorkspaceSession) niuniuAgentResolvePermission(ctx context.Context, req agentbackend.PermissionRequest) (agentbackend.PermissionDecision, error) {
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
		"niuniu-agent:acp_permission", input,
	)
	if err != nil {
		return agentbackend.PermissionDecision{Cancelled: true}, err
	}
	if behavior == "allow" {
		return agentbackend.PermissionDecision{Confirmed: true}, nil
	}
	return agentbackend.PermissionDecision{Cancelled: true}, nil
}
