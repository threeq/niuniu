package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/niuniu-dev/niuniu/internal/notify"
	"github.com/niuniu-dev/niuniu/internal/service"
)

const (
	notifyWriteWait = 10 * time.Second
	notifyPongWait  = 35 * time.Second
)

type NotifyHandler struct {
	hub   *notify.NotificationHub
	Authz *service.Authz
}

func NewNotifyHandler(hub *notify.NotificationHub) *NotifyHandler {
	return &NotifyHandler{hub: hub}
}

// Connect upgrades HTTP to WebSocket and bridges to NotificationHub.
func (h *NotifyHandler) Connect(c *gin.Context) {
	windowId := c.Query("windowId")
	if windowId == "" {
		windowId = uuid.NewString()
	}

	// Authz subscribe-time gate: when auth is enforced, require a resolved user
	// identity before allowing the connection.
	userID := c.GetInt64("auth_user_id")
	if h.Authz != nil && userID == 0 {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	// Compute accessible owners at connect time for per-event owner filtering
	// (spec §5.9). For unauthenticated connections (auth disabled) orgIDs stays
	// nil which causes canSeeOwner to accept any org-scoped event.
	var orgIDs []int64
	if h.Authz != nil && userID != 0 {
		owners, err := h.Authz.Accessible(context.Background(), userID)
		if err != nil {
			slog.Warn("notify: failed to resolve accessible owners", "userID", userID, "err", err)
			// Fall through with empty orgIDs — personal-only filtering applies
		} else {
			orgIDs = owners.OrgIDs
		}
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		slog.Error("notify ws upgrade failed", "error", err)
		return
	}

	ch := h.hub.Register(windowId, userID, orgIDs)
	slog.Info("notify ws connected", "windowId", windowId, "userID", userID)

	defer func() {
		h.hub.Unregister(windowId)
		conn.Close()
		slog.Info("notify ws disconnected", "windowId", windowId)
	}()

	// Read goroutine — handles pong messages, detects client disconnect
	go func() {
		conn.SetReadDeadline(time.Now().Add(notifyPongWait))
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			conn.SetReadDeadline(time.Now().Add(notifyPongWait))
			var m struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(msg, &m) == nil && m.Type == "pong" {
				continue
			}
		}
	}()

	// Write loop — sends notifications from hub channel
	for data := range ch {
		conn.SetWriteDeadline(time.Now().Add(notifyWriteWait))
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			slog.Warn("notify ws write error", "windowId", windowId, "error", err)
			return
		}
	}
}
