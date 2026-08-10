package notify

import (
	"sync"
	"time"
)

// BgTaskDebouncer coalesces bursts of bg-task changes per workspace into a
// single notify emit. Different from notify.Debouncer (which is bound to a hub
// and fires diff/git_status); this one accepts a generic emit callback so it
// stays decoupled from agentproxy.
type BgTaskDebouncer struct {
	delay  time.Duration
	emit   func(Notification)
	mu     sync.Mutex
	timers map[int64]*time.Timer
}

// NewBgTaskDebouncer creates a debouncer with the given window. The emit
// callback receives a Notification with Topic = TopicWorkspaceBgTask.
func NewBgTaskDebouncer(delay time.Duration, emit func(Notification)) *BgTaskDebouncer {
	return &BgTaskDebouncer{
		delay:  delay,
		emit:   emit,
		timers: make(map[int64]*time.Timer),
	}
}

// Notify schedules a debounced emit for the workspace. Multiple Notify calls
// inside the delay window collapse to one emit. The resolveOwner callback is
// invoked at fire time (not at Notify time) so callers that need a SQL or
// other expensive lookup pay the cost at most once per debounced fire (~5/s
// per workspace at 200ms delay). If resolveOwner returns ok=false, the emit
// is skipped — useful for "owner unknown / unsafe to broadcast" branches.
func (d *BgTaskDebouncer) Notify(workspaceID int64, resolveOwner func() (ownerType string, ownerID int64, ok bool)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if t, ok := d.timers[workspaceID]; ok {
		t.Stop()
	}
	d.timers[workspaceID] = time.AfterFunc(d.delay, func() {
		ownerType, ownerID, ok := resolveOwner()
		d.mu.Lock()
		delete(d.timers, workspaceID)
		d.mu.Unlock()
		if !ok || ownerType == "" {
			return
		}
		d.emit(Notification{
			Topic:     TopicWorkspaceBgTask,
			Action:    "changed",
			ID:        workspaceID,
			OwnerType: ownerType,
			OwnerID:   ownerID,
		})
	})
}
