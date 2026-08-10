package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// TeamHandler exposes per-workspace blackboard + inbox endpoints. Coordination
// happens via MCP tools driven by the agent; these endpoints just project the
// resulting rows in blackboard_entries / inbox.
type TeamHandler struct {
	Q            *store.Queries
	WorkspaceSvc *service.WorkspaceService
	EventBus     *event.Bus
	InboxSvc     *service.InboxService
	Authz        *service.Authz
}

// GET /api/workspaces/:id/team/blackboard
// Supports query params: ?key=<key> for single entry, ?type=<type> for filtering.
func (h *TeamHandler) ListBlackboard(c *gin.Context) {
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	entries, err := h.Q.ListBlackboardEntries(c.Request.Context(), wsID)
	if err != nil {
		InternalError(c, err)
		return
	}

	// Filter by key (single entry lookup)
	if key := c.Query("key"); key != "" {
		for _, e := range entries {
			if e.EntryKey == key {
				c.JSON(http.StatusOK, e)
				return
			}
		}
		c.JSON(http.StatusOK, nil)
		return
	}

	// Filter by type
	if typeFilter := c.Query("type"); typeFilter != "" {
		filtered := make([]store.BlackboardEntry, 0)
		for _, e := range entries {
			if e.EntryType == typeFilter {
				filtered = append(filtered, e)
			}
		}
		c.JSON(http.StatusOK, filtered)
		return
	}

	c.JSON(http.StatusOK, entries)
}

// POST /api/workspaces/:id/team/blackboard
func (h *TeamHandler) WriteBlackboard(c *gin.Context) {
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	var body struct {
		Key      string `json:"key" binding:"required"`
		Type     string `json:"type" binding:"required"`
		Content  string `json:"content" binding:"required"`
		Producer string `json:"producer"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if body.Producer == "" {
		body.Producer = "agent"
	}
	_, err = h.Q.UpsertBlackboardEntry(c.Request.Context(), store.UpsertBlackboardEntryParams{
		WorkspaceID:   wsID,
		ProducerAgent: body.Producer,
		EntryType:     body.Type,
		EntryKey:      body.Key,
		Content:       body.Content,
		Metadata:      "{}",
	})
	if err != nil {
		InternalError(c, err)
		return
	}

	if h.EventBus != nil {
		payload, _ := json.Marshal(map[string]string{
			"key":      body.Key,
			"type":     body.Type,
			"producer": body.Producer,
		})
		h.EventBus.Publish(event.OutputEvent{
			Type:        event.EventTeamBlackboardUpdated,
			Content:     string(payload),
			WorkspaceId: wsID,
			Ts:          time.Now().UnixMilli(),
		})

		// When coordinator writes pipeline phase status, also emit harness_status
		// so the chat-input badge updates in real-time.
		if body.Type == "status" && strings.Contains(body.Key, "/current-phase") {
			h.EventBus.Publish(event.OutputEvent{
				Type:          event.EventHarnessStatus,
				WorkspaceId:   wsID,
				Ts:            time.Now().UnixMilli(),
				HarnessStatus: &event.HarnessStatusData{
					Status: "running",
					Phase:  body.Key,
				},
			})
		}
	}

	c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// InboxSend handles POST /mcp/inbox/send.
// Request body: {workspace_id, to, text}. The sender ("from") is read from
// the X-Niuniu-Agent HTTP header; the body cannot spoof it.
func (h *TeamHandler) InboxSend(c *gin.Context) {
	var req struct {
		WorkspaceID int64  `json:"workspace_id"`
		To          string `json:"to"`
		Text        string `json:"text"`
	}
	if err := c.BindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	from := c.GetHeader("X-Niuniu-Agent")
	if from == "" {
		BadRequest(c, "missing X-Niuniu-Agent header")
		return
	}
	if err := h.InboxSvc.Send(c.Request.Context(), req.WorkspaceID, from, req.To, req.Text); err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "sent"})
}

// InboxRead handles POST /mcp/inbox/read.
// Request body: {workspace_id, agent}. Returns unread messages and marks
// them read on disk.
func (h *TeamHandler) InboxRead(c *gin.Context) {
	var req struct {
		WorkspaceID int64  `json:"workspace_id"`
		Agent       string `json:"agent"`
	}
	if err := c.BindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	msgs, err := h.InboxSvc.Read(c.Request.Context(), req.WorkspaceID, req.Agent)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, msgs)
}

