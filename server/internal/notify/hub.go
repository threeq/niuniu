package notify

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"
)

const (
	channelBufferSize = 64
	pingInterval      = 15 * time.Second
)

// connMeta holds per-connection metadata used for owner-based event filtering.
type connMeta struct {
	ch        chan []byte
	userID    int64
	orgIDs    []int64 // org IDs the user belongs to (empty = personal-only user)
}

// canSeeOwner returns true if the connection is allowed to receive a notification
// from the given (ownerType, ownerID) pair.
//
// Rules (spec §5.9):
//   - ownerType == "" → always visible (unknown owner; preserves pre-migration behaviour)
//   - ownerType == "user" → visible only when meta.userID == ownerID
//   - ownerType == "org" → visible when ownerID is in meta.orgIDs
func (m *connMeta) canSeeOwner(ownerType string, ownerID int64) bool {
	if ownerType == "" {
		return true
	}
	if ownerType == "user" {
		return m.userID == ownerID
	}
	// org
	for _, id := range m.orgIDs {
		if id == ownerID {
			return true
		}
	}
	return false
}

// NotificationHub manages WebSocket notification connections and broadcasts.
// Per-event owner filtering is applied at fanout time based on metadata
// stored at connection registration time.
type NotificationHub struct {
	conns     map[string]*connMeta // windowId → connection metadata
	mu        sync.RWMutex
	stop      chan struct{}
}

func NewNotificationHub() *NotificationHub {
	h := &NotificationHub{
		conns: make(map[string]*connMeta),
		stop:  make(chan struct{}),
	}
	go h.pingLoop()
	return h
}

// Register adds a client and returns its message channel.
// userID is 0 when auth is disabled or the user is not authenticated.
// orgIDs is the list of org IDs the user belongs to (used for owner filtering).
func (h *NotificationHub) Register(windowId string, userID int64, orgIDs []int64) chan []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.conns[windowId]; ok {
		close(old.ch)
	}
	ch := make(chan []byte, channelBufferSize)
	h.conns[windowId] = &connMeta{
		ch:     ch,
		userID: userID,
		orgIDs: orgIDs,
	}
	slog.Info("NotifyHub: register", "windowId", windowId, "userID", userID, "total", len(h.conns))
	return ch
}

// Unregister removes a client and closes its channel.
func (h *NotificationHub) Unregister(windowId string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if m, ok := h.conns[windowId]; ok {
		close(m.ch)
		delete(h.conns, windowId)
		slog.Info("NotifyHub: unregister", "windowId", windowId, "total", len(h.conns))
	}
}

// Broadcast sends a notification to connected clients that are allowed to see it.
// If n.OwnerType is empty, the notification is sent to all clients (global broadcast).
// Otherwise only clients whose accessible-owner set includes (n.OwnerType, n.OwnerID)
// receive the notification.
func (h *NotificationHub) Broadcast(n Notification) {
	data, err := json.Marshal(n)
	if err != nil {
		slog.Error("NotifyHub: marshal error", "error", err)
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for windowId, m := range h.conns {
		if !m.canSeeOwner(n.OwnerType, n.OwnerID) {
			continue
		}
		select {
		case m.ch <- data:
		default:
			slog.Warn("NotifyHub: channel full, dropping", "windowId", windowId, "topic", n.Topic)
		}
	}
}

func (h *NotificationHub) pingLoop() {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	ping := Notification{Topic: TopicPing}
	for {
		select {
		case <-ticker.C:
			h.Broadcast(ping)
		case <-h.stop:
			return
		}
	}
}

// DisconnectUser closes all connections whose context carries the given userID.
// Non-blocking: the channel is closed which causes the write-loop in the handler
// to exit naturally.
func (h *NotificationHub) DisconnectUser(userID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for windowId, m := range h.conns {
		if m.userID == userID {
			close(m.ch)
			delete(h.conns, windowId)
			slog.Info("NotifyHub: disconnected user session", "windowId", windowId, "userID", userID)
		}
	}
}

// Stop shuts down the ping loop and closes all client channels.
func (h *NotificationHub) Stop() {
	close(h.stop)
	h.mu.Lock()
	defer h.mu.Unlock()
	for windowId, m := range h.conns {
		close(m.ch)
		delete(h.conns, windowId)
	}
}
