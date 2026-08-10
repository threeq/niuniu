package agentproxy

import (
	"log/slog"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/event"
)

// Compile-time check: SessionHub must have a Broadcast(int64, event.OutputEvent) method
// so it can satisfy loop.EventBroadcaster via structural typing.
var _ interface {
	Broadcast(int64, event.OutputEvent)
} = (*SessionHub)(nil)

const replayBufferSize = 500 // max events kept per workspace for replay on reconnect

// SessionHub manages SSE subscription rooms per workspace.
// It is safe for concurrent use.
type SessionHub struct {
	// rooms: workspaceID → (windowId → channel)
	rooms map[int64]map[string]chan OutputEvent
	// replay: workspaceID → ring buffer of recent events (for reconnecting clients)
	replay map[int64]*replayBuffer
	mu     sync.RWMutex
	stop   chan struct{} // closed when hub is shut down
}

// replayBuffer is a bounded FIFO buffer of OutputEvents.
type replayBuffer struct {
	events []OutputEvent
}

func (rb *replayBuffer) push(ev OutputEvent) {
	rb.events = append(rb.events, ev)
	if len(rb.events) > replayBufferSize {
		// Drop oldest 20% to avoid frequent trimming
		drop := replayBufferSize / 5
		rb.events = rb.events[drop:]
	}
}

// eventsSince returns all events with Ts > sinceMs. If sinceMs is 0, returns all.
func (rb *replayBuffer) eventsSince(sinceMs int64) []OutputEvent {
	if sinceMs == 0 {
		return rb.events
	}
	for i, ev := range rb.events {
		if ev.Ts > sinceMs {
			return rb.events[i:]
		}
	}
	return nil
}

func (rb *replayBuffer) clear() {
	rb.events = rb.events[:0]
}

// NewSessionHub creates a new SessionHub with its ping loop running.
func NewSessionHub() *SessionHub {
	h := &SessionHub{
		rooms:  make(map[int64]map[string]chan OutputEvent),
		replay: make(map[int64]*replayBuffer),
		stop:   make(chan struct{}),
	}
	go h.pingLoop()
	return h
}

// Subscribe adds a channel for a windowId under a workspace room.
// Returns the channel that will receive OutputEvents.
func (h *SessionHub) Subscribe(workspaceID int64, windowId string) chan OutputEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.rooms[workspaceID] == nil {
		h.rooms[workspaceID] = make(map[string]chan OutputEvent)
	}
	ch := make(chan OutputEvent, 256)
	h.rooms[workspaceID][windowId] = ch
	slog.Info("Hub: Subscribe", "workspaceID", workspaceID, "windowId", windowId, "roomSize", len(h.rooms[workspaceID]))
	return ch
}

// Replay sends buffered events since sinceMs to the given channel.
// Call after Subscribe to catch up on missed events during disconnection.
func (h *SessionHub) Replay(workspaceID int64, ch chan OutputEvent, sinceMs int64) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	rb := h.replay[workspaceID]
	if rb == nil {
		return 0
	}
	events := rb.eventsSince(sinceMs)
	for _, ev := range events {
		select {
		case ch <- ev:
		default:
			// Channel full during replay — stop
			return len(events)
		}
	}
	return len(events)
}

// Unsubscribe removes a windowId from a workspace room and closes its channel.
// Also cleans up the room map entry if no subscribers remain.
func (h *SessionHub) Unsubscribe(workspaceID int64, windowId string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ch, ok := h.rooms[workspaceID][windowId]; ok {
		close(ch)
		delete(h.rooms[workspaceID], windowId)
		slog.Info("Hub: Unsubscribe", "workspaceID", workspaceID, "windowId", windowId, "roomSize", len(h.rooms[workspaceID]))
		// Clean up empty room
		if len(h.rooms[workspaceID]) == 0 {
			delete(h.rooms, workspaceID)
		}
	}
}

// Broadcast sends an event to all subscribers of a workspace and buffers it for replay.
func (h *SessionHub) Broadcast(workspaceID int64, event OutputEvent) {
	h.mu.Lock()
	// Buffer for replay (skip pings)
	if event.Type != EventPing {
		if h.replay[workspaceID] == nil {
			h.replay[workspaceID] = &replayBuffer{}
		}
		h.replay[workspaceID].push(event)
	}
	h.mu.Unlock()

	h.mu.RLock()
	defer h.mu.RUnlock()
	subCount := 0
	for windowId, ch := range h.rooms[workspaceID] {
		select {
		case ch <- event:
			subCount++
		default:
			slog.Warn("SSE subscriber channel full, dropping event", "workspaceID", workspaceID, "windowId", windowId, "eventType", event.Type)
		}
	}
	if subCount == 0 {
		slog.Debug("SSE broadcast: no subscribers (buffered for replay)", "workspaceID", workspaceID, "eventType", event.Type)
	}
}

// DisconnectUser closes all SSE streams belonging to the given user.
//
// TODO(FU-1/F-3): The SessionHub is currently keyed by (workspaceID, windowId)
// with no per-connection userID index. To implement true per-user SSE teardown
// we need to record userID alongside each subscription at Subscribe() time and
// iterate here. Until that index is added this method is a no-op — the
// connection will naturally expire when its workspace heartbeat times out.
// The notify-hub (NotificationHub.DisconnectUser) does perform real teardown
// because it gained a userIndex in the same fix batch (FU-1 F-3).
func (h *SessionHub) DisconnectUser(_ int64) {
	// no-op: see TODO above.
}

// ClearReplay removes the replay buffer for a workspace (call on session clear).
func (h *SessionHub) ClearReplay(workspaceID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if rb := h.replay[workspaceID]; rb != nil {
		rb.clear()
	}
}

// RemoveWorkspace removes all resources for a workspace: subscribers, replay buffer.
// Used when a workspace is deleted.
func (h *SessionHub) RemoveWorkspace(workspaceID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Close all subscriber channels
	for _, ch := range h.rooms[workspaceID] {
		close(ch)
	}
	delete(h.rooms, workspaceID)
	delete(h.replay, workspaceID)
}

// pingLoop runs every 30s to detect and remove stale subscribers.
func (h *SessionHub) pingLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			h.cleanStale()
		case <-h.stop:
			return
		}
	}
}

func (h *SessionHub) cleanStale() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for workspaceID, subs := range h.rooms {
		for windowId, ch := range subs {
			select {
			case ch <- OutputEvent{Type: EventPing}:
			default:
				close(ch)
				delete(h.rooms[workspaceID], windowId)
				slog.Debug("stale SSE subscriber removed", "workspaceID", workspaceID, "windowId", windowId)
			}
		}
		// Clean up empty room after removing stale subscribers
		if len(h.rooms[workspaceID]) == 0 {
			delete(h.rooms, workspaceID)
		}
	}
}

// Stop shuts down the ping loop.
func (h *SessionHub) Stop() {
	close(h.stop)
}
