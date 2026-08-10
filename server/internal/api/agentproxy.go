package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/niuniu-dev/niuniu/internal/agentproxy"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

type AgentProxyHandler struct {
	proxy        *agentproxy.AgentProxy
	workspaceSvc *service.WorkspaceService
	Authz        *service.Authz
}

func NewAgentProxyHandler(proxy *agentproxy.AgentProxy, workspaceSvc *service.WorkspaceService) *AgentProxyHandler {
	return &AgentProxyHandler{proxy: proxy, workspaceSvc: workspaceSvc}
}

// GET /workspaces/:id/messages?limit=500&before=<msgId>&after=<msgId>
//
// Pagination contract:
//   - no cursor: latest `limit` messages, ASC order.
//   - ?before=<id>: page older — `limit` messages strictly older than <id>.
//   - ?after=<id>: page newer — strictly newer than <id>, ASC order. Used by
//     mobile useWorkspaceSSE for incremental polling so a 2 s tick stops
//     re-shipping the entire history each time.
//
// `before` and `after` are mutually exclusive. If both are present, `after`
// wins (the newer-than path is the cheap one and the only one a polling
// client should be sending).
func (h *AgentProxyHandler) ListMessages(c *gin.Context) {
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}

	limit := int64(500)
	if l := c.Query("limit"); l != "" {
		if parsed, err := strconv.ParseInt(l, 10, 64); err == nil && parsed > 0 && parsed <= 2000 {
			limit = parsed
		}
	}

	ctx := c.Request.Context()
	var messages []agentMessageResponse
	var hasMore bool

	afterID := c.Query("after")
	beforeID := c.Query("before")
	if afterID != "" {
		result, err := h.proxy.Q().ListAgentMessagesAfter(ctx, store.ListAgentMessagesAfterParams{
			WorkspaceID: workspaceID,
			ID:          afterID,
			ID_2:        afterID,
			ID_3:        afterID,
			Limit:       limit + 1,
		})
		if err != nil {
			InternalError(c, fmt.Errorf("failed to list messages: %w", err))
			return
		}
		hasMore = int64(len(result)) > limit
		if hasMore {
			result = result[:limit] // drop the newest extra row, keep ASC order
		}
		for _, m := range result {
			messages = append(messages, toAgentMessageResponse(m))
		}
	} else if beforeID != "" {
		result, err := h.proxy.Q().ListAgentMessagesBefore(ctx, store.ListAgentMessagesBeforeParams{
			WorkspaceID: workspaceID,
			ID:          beforeID,
			ID_2:        beforeID,
			ID_3:        beforeID,
			Limit:       limit + 1,
		})
		if err != nil {
			InternalError(c, fmt.Errorf("failed to list messages: %w", err))
			return
		}
		hasMore = int64(len(result)) > limit
		if hasMore {
			result = result[1:] // drop the oldest extra row, keep the rest in ASC order
		}
		for _, m := range result {
			messages = append(messages, toAgentMessageResponse(m))
		}
	} else {
		// Fetch the latest N messages (newest first internally, returned in ASC order)
		result, err := h.proxy.Q().ListAgentMessagesLatest(ctx, store.ListAgentMessagesLatestParams{
			WorkspaceID: workspaceID,
			Limit:       limit + 1,
		})
		if err != nil {
			InternalError(c, fmt.Errorf("failed to list messages: %w", err))
			return
		}
		hasMore = int64(len(result)) > limit
		if hasMore {
			result = result[1:] // drop the oldest extra row, keep the rest in ASC order
		}
		for _, m := range result {
			messages = append(messages, toAgentMessageResponse(m))
		}
	}

	if messages == nil {
		messages = []agentMessageResponse{}
	}

	c.JSON(http.StatusOK, gin.H{"messages": messages, "hasMore": hasMore})
}

type agentMessageResponse struct {
	ID           string  `json:"id"`
	WorkspaceID  int64   `json:"workspaceId"`
	Role         string  `json:"role"`
	Content      string  `json:"content"`
	MessageID    string  `json:"messageId"`
	EventType    string  `json:"eventType"`
	ToolName     string  `json:"toolName,omitempty"`
	ToolInput    string  `json:"toolInput,omitempty"`
	ToolUseID    string  `json:"toolUseId,omitempty"`
	IsError      bool    `json:"isError,omitempty"`
	CostUsd      float64 `json:"costUsd,omitempty"`
	NumTurns     int     `json:"numTurns,omitempty"`
	DurationMs   int64   `json:"durationMs,omitempty"`
	InputTokens  int64   `json:"inputTokens,omitempty"`
	OutputTokens int64   `json:"outputTokens,omitempty"`
	Attachments  string  `json:"attachments,omitempty"`
	CreatedAt    int64   `json:"createdAt"`
}

func toAgentMessageResponse(m store.AgentMessage) agentMessageResponse {
	return agentMessageResponse{
		ID:           m.ID,
		WorkspaceID:  m.WorkspaceID,
		Role:         m.Role,
		Content:      m.Content,
		MessageID:    m.MessageID,
		EventType:    m.EventType,
		ToolName:     m.ToolName.String,
		ToolInput:    m.ToolInput.String,
		ToolUseID:    m.ToolUseID.String,
		IsError:      m.IsError != 0,
		CostUsd:      m.CostUsd.Float64,
		NumTurns:     int(m.NumTurns.Int64),
		DurationMs:   m.DurationMs.Int64,
		InputTokens:  m.InputTokens.Int64,
		OutputTokens: m.OutputTokens.Int64,
		Attachments:  m.Attachments.String,
		CreatedAt:    m.CreatedAt.UnixMilli(),
	}
}

// POST /workspaces/:id/messages  Body: {"content": "..."}
func (h *AgentProxyHandler) SendMessage(c *gin.Context) {
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}

	// Authz gate: reject a caller who cannot access this workspace before we do
	// anything else. This matters doubly here because SetSessionUser (below)
	// PERSISTS the caller as current_session_user_id, which then scopes
	// credential-bound MCP tools — an unauthorized cross-tenant POST must never
	// stamp its identity onto someone else's workspace. Mirrors the PTY send
	// gate (agent.go) and no-ops when auth is disabled (Authz nil / userID 0).
	if h.Authz != nil {
		if userID := c.GetInt64("auth_user_id"); userID != 0 {
			if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, workspaceID); err != nil {
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
		}
	}

	if err := h.workspaceSvc.CheckNotArchived(c.Request.Context(), workspaceID); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}

	var req struct {
		Content     string `json:"content" binding:"required"`
		Attachments []struct {
			Path     string `json:"path"`
			Type     string `json:"type"`
			Name     string `json:"name"`
			MimeType string `json:"mimeType,omitempty"`
			Size     int64  `json:"size,omitempty"`
		} `json:"attachments"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "content is required")
		return
	}

	// Serialize attachments JSON for DB storage
	var attachmentsJSON string
	if len(req.Attachments) > 0 {
		b, _ := json.Marshal(req.Attachments)
		attachmentsJSON = string(b)
	}

	finalContent := req.Content
	if len(req.Attachments) > 0 {
		var paths []string
		for _, a := range req.Attachments {
			paths = append(paths, a.Path)
		}
		finalContent = req.Content + "\n\n[附件: " + strings.Join(paths, ", ") + "]"
	}

	ctx := c.Request.Context()

	// Get workspace path from DB
	ws, err := h.proxy.Q().GetWorkspace(ctx, workspaceID)
	if err != nil {
		InternalError(c, fmt.Errorf("workspace not found: %w", err))
		return
	}

	slog.Info("SendMessage: workspace found", "workspaceID", workspaceID, "path", ws.Path)

	// Record the authenticated caller as the workspace's current session user.
	// The proxy chat send path starts sessions via Deliver(userID=0), so without
	// this the identity stays NULL and MCPTokenAuth can't resolve auth_user_id on
	// org-owned (team-edition) workspaces — 401ing every credential-scoped MCP
	// tool (external-proxy / data-proxy / dashboards). Mirrors the PTY path.
	h.proxy.SetSessionUser(ctx, workspaceID, c.GetInt64("auth_user_id"))

	// A manual user send is an explicit "take over now": if the workspace only
	// LOOKS idle (no live loop) but a scheduled autohost resume / pending wakeup
	// is holding the Enqueue gate closed, open it and cancel the queued resume so
	// this message runs immediately instead of silently queueing until that resume
	// fires. No-op while a loop is live (the message correctly queues behind it).
	h.proxy.PrepareUserSend(ctx, workspaceID)

	queued, queueID, err := h.proxy.Deliver(ctx, workspaceID, ws.Path, finalContent, attachmentsJSON)
	if err != nil {
		InternalError(c, fmt.Errorf("failed to deliver message: %w", err))
		return
	}
	if queued {
		slog.Info("SendMessage: queued (session busy)", "workspaceID", workspaceID, "queueId", queueID)
		c.JSON(http.StatusAccepted, gin.H{"status": "queued", "queueId": queueID})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{"status": "accepted"})
}

// GET /workspaces/:id/session
func (h *AgentProxyHandler) GetSession(c *gin.Context) {
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}

	session := h.proxy.GetSession(workspaceID)
	if session == nil {
		c.JSON(http.StatusOK, gin.H{
			"sessionId": nil,
			"status":    "idle",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"sessionId": session.SessionID(),
		"status":    string(session.Status()),
		"workDir":   session.WorkDir(),
	})
}

// DELETE /workspaces/:id/session — stop the running process
func (h *AgentProxyHandler) StopSession(c *gin.Context) {
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}

	if err := h.workspaceSvc.CheckNotArchived(c.Request.Context(), workspaceID); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}

	ctx := c.Request.Context()
	session := h.proxy.GetSession(workspaceID)
	if session == nil {
		// No in-memory session, but broadcast idle so the frontend unblocks
		// if it was showing a running state (e.g. after a server restart).
		h.proxy.GetHub().Broadcast(workspaceID, agentproxy.NewOutputEvent(agentproxy.EventIdle, "", "", "system", workspaceID))
		c.Status(http.StatusNoContent)
		return
	}

	session.Stop(ctx)
	c.Status(http.StatusNoContent)
}

// POST /workspaces/:id/session/clear — start a new conversation
func (h *AgentProxyHandler) ClearSession(c *gin.Context) {
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}

	if err := h.workspaceSvc.CheckNotArchived(c.Request.Context(), workspaceID); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}

	session := h.proxy.GetSession(workspaceID)
	if session != nil {
		session.ClearSession(c.Request.Context())
	}
	c.Status(http.StatusNoContent)
}

// GET /workspaces/:id/costs — permanent cost records
func (h *AgentProxyHandler) GetCosts(c *gin.Context) {
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}

	costs, err := h.proxy.Q().ListWorkspaceCosts(c.Request.Context(), workspaceID)
	if err != nil {
		InternalError(c, err)
		return
	}

	var totalCost float64
	var totalTurns int64
	var totalDurationMs int64
	for _, cost := range costs {
		totalCost += cost.CostUsd
		totalTurns += cost.NumTurns
		totalDurationMs += cost.DurationMs
	}

	type costEntry struct {
		CostUsd    float64 `json:"costUsd"`
		NumTurns   int64   `json:"numTurns"`
		DurationMs int64   `json:"durationMs"`
		SessionID  string  `json:"sessionId,omitempty"`
		CreatedAt  int64   `json:"createdAt"`
	}

	entries := make([]costEntry, len(costs))
	for i, c := range costs {
		entries[i] = costEntry{
			CostUsd:    c.CostUsd,
			NumTurns:   c.NumTurns,
			DurationMs: c.DurationMs,
			SessionID:  c.SessionID.String,
			CreatedAt:  c.CreatedAt.UnixMilli(),
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"costs":             entries,
		"totalCostUsd":      totalCost,
		"totalTurns":        totalTurns,
		"totalDurationMs":   totalDurationMs,
		"totalInteractions": len(costs),
	})
}

// GET /workspaces/:id/claude-status — computed usage data from session state
func (h *AgentProxyHandler) GetClaudeStatus(c *gin.Context) {
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}

	session := h.proxy.GetSession(workspaceID)
	if session == nil {
		c.JSON(http.StatusOK, gin.H{"status": "no_session"})
		return
	}

	status := session.GetClaudeStatus(c.Request.Context())
	c.JSON(http.StatusOK, status)
}

// GET /ws/sse — SSE stream
// Query params:
//
//	?windowId=<uuid>   — browser window identifier (optional, generated if absent)
//	?workspaces=1,2,3  — comma-separated workspace IDs to subscribe to
func (h *AgentProxyHandler) SSE(c *gin.Context) {
	windowId := c.Query("windowId")
	if windowId == "" {
		windowId = uuid.NewString()
	}

	// Resolve caller identity (set by IdentityResolver middleware).
	userID := c.GetInt64("auth_user_id")

	workspacesRaw := c.Query("workspaces")
	var requestedIDs []int64
	if workspacesRaw != "" {
		for _, part := range strings.Split(workspacesRaw, ",") {
			if id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64); err == nil {
				requestedIDs = append(requestedIDs, id)
			}
		}
	}

	// Authz subscribe-time gate: drop workspace IDs the caller cannot access.
	// If Authz is configured (auth enabled), filter the requested IDs.
	var subscribedIDs []int64
	if h.Authz != nil && userID != 0 {
		ctx := c.Request.Context()
		for _, wsID := range requestedIDs {
			if _, err := h.Authz.CanAccessWorkspace(ctx, userID, wsID); err != nil {
				slog.Debug("SSE: dropping unauthorized workspace", "userID", userID, "workspaceID", wsID, "err", err)
				continue
			}
			subscribedIDs = append(subscribedIDs, wsID)
		}
		if len(requestedIDs) > 0 && len(subscribedIDs) == 0 {
			c.Header("Content-Type", "text/event-stream")
			c.Header("Cache-Control", "no-cache")
			c.Header("Connection", "keep-alive")
			c.Header("X-Window-Id", windowId)
			c.Writer.Write([]byte("data: {\"error\":\"forbidden\"}\n\n"))
			c.Writer.(http.Flusher).Flush()
			return
		}
	} else {
		// Auth not enforced — subscribe to all requested IDs as-is.
		subscribedIDs = requestedIDs
	}

	// Build per-connection authorized ID set for per-event filtering.
	// This snapshot is refreshed every 30 seconds to detect mid-stream revocations.
	authorizedSet := make(map[int64]struct{}, len(subscribedIDs))
	for _, id := range subscribedIDs {
		authorizedSet[id] = struct{}{}
	}
	lastAuthzRefresh := time.Now()

	// Parse "since" timestamp for replay on reconnect
	var sinceMs int64
	if s := c.Query("since"); s != "" {
		sinceMs, _ = strconv.ParseInt(s, 10, 64)
	}

	// Subscribe to each workspace room and replay missed events
	hub := h.proxy.GetHub()
	var subs []wsSub
	for _, wsID := range subscribedIDs {
		ch := hub.Subscribe(wsID, windowId)
		if sinceMs > 0 {
			replayed := hub.Replay(wsID, ch, sinceMs)
			if replayed > 0 {
				slog.Info("SSE replay", "workspaceID", wsID, "windowId", windowId, "events", replayed, "sinceMs", sinceMs)
			}
		}
		subs = append(subs, wsSub{wsID, ch})
	}

	// Ensure subscriptions are cleaned up on any exit path (panic, early return, etc.)
	defer func() {
		for _, s := range subs {
			hub.Unsubscribe(s.wsID, windowId)
		}
	}()

	slog.Info("SSE connection opened", "windowId", windowId, "workspaces", subscribedIDs, "sinceMs", sinceMs)

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Window-Id", windowId)

	clientGone := c.Request.Context().Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	flusher := c.Writer.(http.Flusher)

	// Merge all workspace channels into one with a done signal for clean shutdown.
	done := make(chan struct{})
	merged := mergeChannels(subs, done)

	// Track workspaces for which we have already sent workspace_auth_error so
	// we emit the control message at most once per (connection, workspaceID).
	notifiedAuthErrors := make(map[int64]bool)

	// refreshAuthorizedSet rebuilds the per-connection authorized-ID snapshot by
	// re-checking each subscribed workspace. IDs that were previously allowed but
	// are no longer accessible (revoked membership) trigger a workspace_auth_error
	// control event and are removed from the live set.
	refreshAuthorizedSet := func() {
		if h.Authz == nil || userID == 0 || len(subscribedIDs) == 0 {
			return
		}
		bgCtx := context.Background()
		newSet := make(map[int64]struct{}, len(subscribedIDs))
		for _, wsID := range subscribedIDs {
			if _, err := h.Authz.CanAccessWorkspace(bgCtx, userID, wsID); err == nil {
				newSet[wsID] = struct{}{}
			}
		}
		// Detect revocations: IDs in old set but not in new set.
		for wsID := range authorizedSet {
			if _, still := newSet[wsID]; !still {
				slog.Info("SSE: workspace access revoked mid-stream", "windowId", windowId, "workspaceID", wsID, "userID", userID)
				if !notifiedAuthErrors[wsID] {
					notifiedAuthErrors[wsID] = true
					msg := fmt.Sprintf(`{"type":"workspace_auth_error","workspaceId":%d}`, wsID)
					c.Writer.Write([]byte("data: " + msg + "\n\n"))
					flusher.Flush()
				}
			}
		}
		authorizedSet = newSet
		lastAuthzRefresh = time.Now()
	}

	for {
		select {
		case <-clientGone:
			slog.Info("SSE client gone, closing", "windowId", windowId)
			// Signal merge goroutines to stop, then defer handles Unsubscribe.
			close(done)
			return
		case <-ticker.C:
			c.Writer.Write([]byte(": ping\n\n"))
			flusher.Flush()
			// Piggyback the 30-second authz refresh on the keepalive ticker to avoid
			// an extra goroutine. This gives a revocation window of ~30s, which is
			// acceptable given the existing 5s LRU cache on CanAccessWorkspace.
			if h.Authz != nil && userID != 0 && time.Since(lastAuthzRefresh) >= 30*time.Second {
				refreshAuthorizedSet()
			}
		case ev, ok := <-merged:
			if !ok {
				slog.Info("SSE mergeChannels closed, exiting", "windowId", windowId)
				return
			}
			// Per-event authz gate: O(1) map lookup against the connection-level
			// snapshot. No per-event DB queries; revocations are detected by the
			// periodic 30s refresh above. Global events (WorkspaceId == 0) are
			// system-scoped — not tied to a specific workspace — and pass through
			// the gate, mirroring the same exception in events.go.
			if len(authorizedSet) > 0 && ev.WorkspaceId != 0 {
				if _, allowed := authorizedSet[ev.WorkspaceId]; !allowed {
					slog.Debug("SSE: dropping event for unauthorized workspace", "windowId", windowId, "workspaceID", ev.WorkspaceId)
					// Notify the frontend once so it can prune its subscription list.
					if !notifiedAuthErrors[ev.WorkspaceId] {
						notifiedAuthErrors[ev.WorkspaceId] = true
						msg := fmt.Sprintf(`{"type":"workspace_auth_error","workspaceId":%d}`, ev.WorkspaceId)
						c.Writer.Write([]byte("data: " + msg + "\n\n"))
						flusher.Flush()
					}
					continue
				}
			}
			slog.Debug("SSE sending event", "windowId", windowId, "eventType", ev.Type, "msgId", ev.MessageId)
			data, _ := json.Marshal(ev)
			c.Writer.Write([]byte("data: " + string(data) + "\n\n"))
			flusher.Flush()
		}
	}
}

// wsSub pairs a workspace ID with its SSE event channel.
type wsSub struct {
	wsID int64
	ch   chan agentproxy.OutputEvent
}

// mergeChannels combines multiple OutputEvent channels into one.
// When done is closed, all merge goroutines exit promptly (even if blocked
// sending to out), preventing goroutine leaks on client disconnect.
func mergeChannels(subs []wsSub, done <-chan struct{}) <-chan agentproxy.OutputEvent {
	out := make(chan agentproxy.OutputEvent, 64)
	var wg sync.WaitGroup
	for _, s := range subs {
		wg.Add(1)
		go func(ch <-chan agentproxy.OutputEvent) {
			defer wg.Done()
			for {
				select {
				case ev, ok := <-ch:
					if !ok {
						return
					}
					select {
					case out <- ev:
					case <-done:
						return
					}
				case <-done:
					return
				}
			}
		}(s.ch)
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}
