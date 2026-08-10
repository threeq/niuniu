package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/service"
)

type EventsHandler struct {
	bus   *event.Bus
	ring  *event.Ring
	Authz *service.Authz
}

func NewEventsHandler(bus *event.Bus) *EventsHandler {
	return &EventsHandler{bus: bus, ring: event.NewRing(1000)}
}

// NewEventsHandlerWithRing creates an EventsHandler that uses the provided Ring
// for Last-Event-ID replay. Primarily used by tests.
func NewEventsHandlerWithRing(bus *event.Bus, ring *event.Ring) *EventsHandler {
	return &EventsHandler{bus: bus, ring: ring}
}

func (h *EventsHandler) Stream(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	// No wildcard CORS — SSE is consumed by same-origin WebView or localhost desktop client

	// Resolve caller identity for per-event authz filtering.
	userID := c.GetInt64("auth_user_id")

	// Build an access-checker closure. When Authz is wired and the user is
	// identified, workspace-scoped events are checked against the caller's
	// accessible workspace set (cached 5 s via AccessCache). Global events
	// (WorkspaceId == 0) are always allowed through.
	var canSeeEvent func(evt event.OutputEvent) bool
	if h.Authz != nil && userID != 0 {
		ctx := c.Request.Context()
		canSeeEvent = func(evt event.OutputEvent) bool {
			if evt.WorkspaceId == 0 {
				return true // global event — always pass through
			}
			_, err := h.Authz.CanAccessWorkspace(ctx, userID, evt.WorkspaceId)
			return err == nil
		}
	} else {
		canSeeEvent = func(_ event.OutputEvent) bool { return true }
	}

	// Parse Last-Event-ID for resume after tunnel reconnect.
	var lastID int64
	if raw := c.GetHeader("Last-Event-ID"); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
			lastID = v
		}
	}

	ch := h.bus.Subscribe()
	defer h.bus.Unsubscribe(ch)

	ctx := c.Request.Context()
	keepalive := time.NewTicker(30 * time.Second)
	defer keepalive.Stop()

	// Replay buffered events that the client missed.
	// We do this before entering the stream loop so replayed events are
	// flushed before any new live events arrive.
	if lastID > 0 {
		missed := h.ring.Since(lastID)
		for _, be := range missed {
			if !canSeeEvent(be.Event) {
				continue
			}
			data, err := json.Marshal(be.Event)
			if err != nil {
				continue
			}
			fmt.Fprintf(c.Writer, "id: %d\nevent: %s\ndata: %s\n\n", be.ID, be.Event.Type, data)
		}
		c.Writer.Flush()
	}

	c.Stream(func(w io.Writer) bool {
		select {
		case <-ctx.Done():
			return false
		case <-keepalive.C:
			fmt.Fprintf(w, "event: ping\ndata: {\"type\":\"ping\"}\n\n")
			return true
		case evt, ok := <-ch:
			if !ok {
				return false
			}
			// Per-event authz gate: drop workspace-scoped events the caller
			// cannot see (handles revocation mid-stream).
			if !canSeeEvent(evt) {
				return true
			}
			data, err := json.Marshal(evt)
			if err != nil {
				slog.Error("marshal event", "error", err)
				return true
			}
			id := h.ring.Append(evt)
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", id, evt.Type, data)
			return true
		}
	})
}
