package notify

import (
	"sync"
	"time"
)

// Debouncer delays notification broadcasts per workspace.
// If multiple calls arrive within the delay window, only the last one fires.
type Debouncer struct {
	hub    *NotificationHub
	delay  time.Duration
	timers map[int64]*time.Timer // workspaceID → pending timer
	mu     sync.Mutex
}

func NewDebouncer(hub *NotificationHub, delay time.Duration) *Debouncer {
	return &Debouncer{
		hub:    hub,
		delay:  delay,
		timers: make(map[int64]*time.Timer),
	}
}

// Notify schedules a debounced broadcast of diff.changed and git_status.changed
// for the given workspace. Subsequent calls within the delay window reset the timer.
func (d *Debouncer) Notify(workspaceID int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if t, ok := d.timers[workspaceID]; ok {
		t.Stop()
	}
	d.timers[workspaceID] = time.AfterFunc(d.delay, func() {
		d.hub.Broadcast(Notification{Topic: TopicDiff, Action: "changed", ID: workspaceID})
		d.hub.Broadcast(Notification{Topic: TopicGitStatus, Action: "changed", ID: workspaceID})
		d.mu.Lock()
		delete(d.timers, workspaceID)
		d.mu.Unlock()
	})
}
