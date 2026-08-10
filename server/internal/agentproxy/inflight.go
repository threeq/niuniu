package agentproxy

import (
	"sort"
	"sync"
	"time"
)

// BgTaskKind enumerates the kinds of background tasks tracked per session.
type BgTaskKind string

const (
	BgTaskBash     BgTaskKind = "bash"     // Bash tool with run_in_background:true
	BgTaskSubagent BgTaskKind = "subagent" // Task tool subagent dispatch
	BgTaskWakeup   BgTaskKind = "wakeup"   // ScheduleWakeup tool call
)

// BgTask is a single in-flight background task entry.
type BgTask struct {
	ToolUseID    string
	Kind         BgTaskKind
	Title        string
	StartedAt    time.Time
	ScheduledFor time.Time // wakeup only: StartedAt + delaySeconds
	// BashID is the shell handle returned by the Bash[bg] spawn tool_result
	// ("Command running in background with ID: <id>"). Only set for kind ==
	// BgTaskBash, and only after the spawn ack is observed. Used to remove
	// the entry when the agent calls KillBash[shell_id].
	// Internal: not surfaced via the API DTO. Don't pull it into
	// BgTaskHighlightMeta or BgTaskAggregateDTO without a multi-tenant /
	// privacy review — shell handles can leak temp paths.
	BashID string
}

// InflightTracker is a goroutine-safe map of background tasks belonging to one
// agentproxy session. The session owns it; lifetime is bounded by the session.
type InflightTracker struct {
	mu    sync.RWMutex
	tasks map[string]*BgTask
}

func NewInflightTracker() *InflightTracker {
	return &InflightTracker{tasks: make(map[string]*BgTask)}
}

// Add records a bash or subagent in-flight task. Re-adding the same toolUseID
// overwrites the existing entry.
func (t *InflightTracker) Add(kind BgTaskKind, toolUseID, title string, startedAt time.Time) {
	if toolUseID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tasks[toolUseID] = &BgTask{
		ToolUseID: toolUseID,
		Kind:      kind,
		Title:     title,
		StartedAt: startedAt,
	}
}

// AddWakeup records a wakeup task; ScheduledFor = startedAt + delay.
func (t *InflightTracker) AddWakeup(toolUseID, reason string, startedAt time.Time, delay time.Duration) {
	if toolUseID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tasks[toolUseID] = &BgTask{
		ToolUseID:    toolUseID,
		Kind:         BgTaskWakeup,
		Title:        reason,
		StartedAt:    startedAt,
		ScheduledFor: startedAt.Add(delay),
	}
}

// Remove deletes the entry. Returns true iff an entry was actually removed —
// callers use this to gate notify emits and avoid jitter on duplicate
// tool_results. Missing IDs are a no-op (returns false).
func (t *InflightTracker) Remove(toolUseID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.tasks[toolUseID]; !ok {
		return false
	}
	delete(t.tasks, toolUseID)
	return true
}

// Get returns a copy of the entry for toolUseID and whether it exists. Callers
// use this to inspect Kind before deciding whether to Remove (tool_result has
// kind-specific semantics — see recordBgTaskResult).
func (t *InflightTracker) Get(toolUseID string) (BgTask, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	v, ok := t.tasks[toolUseID]
	if !ok {
		return BgTask{}, false
	}
	return *v, true
}

// SetBashID records the shell_id returned by the Bash[bg] spawn tool_result.
// No-op if the entry is missing or kind != BgTaskBash. Returns true iff the
// field was set (false on no-op or duplicate set with the same value).
func (t *InflightTracker) SetBashID(toolUseID, bashID string) bool {
	if toolUseID == "" || bashID == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	v, ok := t.tasks[toolUseID]
	if !ok || v.Kind != BgTaskBash {
		return false
	}
	if v.BashID == bashID {
		return false
	}
	v.BashID = bashID
	return true
}

// RemoveByBashID finds the BgTaskBash entry whose BashID matches and deletes
// it. Returns true iff an entry was removed. Used when the agent calls
// KillBash[shell_id] — the shell_id is the BashID, not the tool_use_id of the
// original spawn.
func (t *InflightTracker) RemoveByBashID(bashID string) bool {
	if bashID == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, v := range t.tasks {
		if v.Kind == BgTaskBash && v.BashID == bashID {
			delete(t.tasks, id)
			return true
		}
	}
	return false
}

// Concurrency note: all write methods (Add, AddWakeup, Remove, SetBashID,
// RemoveByBashID, Clear, GCStale) share t.mu in write mode; Get and Snapshot
// take it in read mode. Callers in proxy.go (recordBgTaskResult) compose
// Get→SetBashID and Get→Remove non-atomically — the only realistic race is
// GCStale removing the entry between the two calls, in which case the
// follow-up no-ops, which is the correct outcome. Do NOT split the lock per
// kind without re-deriving this safety argument.

// Snapshot returns a copy of all current tasks for safe iteration outside the lock.
func (t *InflightTracker) Snapshot() []BgTask {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]BgTask, 0, len(t.tasks))
	for _, v := range t.tasks {
		out = append(out, *v)
	}
	return out
}

// ClearBash removes all Bash[bg] entries and returns the removed entries so
// callers can emit notifies / log. Used by the bg-task GC when a process-tree
// probe confirms the CLI process has zero living shell descendants — at that
// point any tracked bash bg is a zombie. Other kinds (Subagent, Wakeup) are
// untouched.
func (t *InflightTracker) ClearBash() []BgTask {
	t.mu.Lock()
	defer t.mu.Unlock()
	var removed []BgTask
	for id, v := range t.tasks {
		if v.Kind == BgTaskBash {
			removed = append(removed, *v)
			delete(t.tasks, id)
		}
	}
	return removed
}

// TrimBashTo reduces the Bash[bg] entry count to target by removing victims
// in this priority order:
//  1. Entries that never received a spawn ack (BashID == "") — those are most
//     likely failed-but-not-errored spawns and are the safest to drop first.
//  2. Oldest StartedAt first — older shells are more likely to have completed
//     by the time the probe runs.
//
// Returns the removed entries. No-op when current bash count <= target.
// Used by the bg-task GC when the process-tree probe shows fewer alive shell
// descendants than tracked: we can't identify WHICH specific shells died
// (bash_id is opaque), but we can keep the displayed count honest by trimming.
func (t *InflightTracker) TrimBashTo(target int) []BgTask {
	if target < 0 {
		target = 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	type candidate struct {
		id   string
		task *BgTask
	}
	var bashes []candidate
	for id, v := range t.tasks {
		if v.Kind == BgTaskBash {
			bashes = append(bashes, candidate{id, v})
		}
	}
	if len(bashes) <= target {
		return nil
	}
	// Sort: no-BashID first (likely failed spawn), then by StartedAt ascending
	// (oldest first — most likely to have completed by now).
	sort.Slice(bashes, func(i, j int) bool {
		ai := bashes[i].task.BashID == ""
		aj := bashes[j].task.BashID == ""
		if ai != aj {
			return ai
		}
		return bashes[i].task.StartedAt.Before(bashes[j].task.StartedAt)
	})
	victims := bashes[:len(bashes)-target]
	removed := make([]BgTask, 0, len(victims))
	for _, v := range victims {
		removed = append(removed, *v.task)
		delete(t.tasks, v.id)
	}
	return removed
}

// CountByKind returns the number of entries of a given kind. Cheap snapshot
// used by the GC probe to decide whether the (potentially slow) process-tree
// walk is even worth doing.
func (t *InflightTracker) CountByKind(kind BgTaskKind) int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	n := 0
	for _, v := range t.tasks {
		if v.Kind == kind {
			n++
		}
	}
	return n
}

// Clear empties the tracker. Called on session close.
func (t *InflightTracker) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tasks = make(map[string]*BgTask)
}

// GCStale removes entries older than per-kind thresholds and returns them.
//   - bash: older than 1h (zombie)
//   - subagent: older than 30m (zombie)
//   - wakeup: ScheduledFor is in the past (expired)
//
// Returns the removed entries so callers can decide whether to log warnings
// or emit notifies.
func (t *InflightTracker) GCStale(now time.Time) []BgTask {
	t.mu.Lock()
	defer t.mu.Unlock()
	var removed []BgTask
	for id, v := range t.tasks {
		stale := false
		switch v.Kind {
		case BgTaskBash:
			stale = now.Sub(v.StartedAt) > 1*time.Hour
		case BgTaskSubagent:
			stale = now.Sub(v.StartedAt) > 30*time.Minute
		case BgTaskWakeup:
			stale = !v.ScheduledFor.IsZero() && v.ScheduledFor.Before(now)
		}
		if stale {
			removed = append(removed, *v)
			delete(t.tasks, id)
		}
	}
	return removed
}
