package event

import "sync"

// BufferedEvent wraps an OutputEvent with a monotonic sequence ID for SSE resume.
type BufferedEvent struct {
	ID    int64
	Event OutputEvent
}

// Ring is a fixed-capacity ring buffer of BufferedEvents used to support
// SSE Last-Event-ID resume. Events are assigned monotonically increasing IDs.
// Capacity defaults to 1000 events. Ring is safe for concurrent use.
type Ring struct {
	mu     sync.Mutex
	buf    []BufferedEvent
	cap    int
	head   int  // index of oldest entry (when full)
	count  int  // number of valid entries
	nextID int64
}

// NewRing creates a Ring with the given capacity (must be > 0).
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		capacity = 1000
	}
	return &Ring{
		buf:    make([]BufferedEvent, capacity),
		cap:    capacity,
		nextID: 1,
	}
}

// Append stores ev in the ring and assigns it the next sequence ID.
// Returns the assigned ID.
func (r *Ring) Append(ev OutputEvent) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := r.nextID
	r.nextID++
	be := BufferedEvent{ID: id, Event: ev}
	if r.count < r.cap {
		r.buf[(r.head+r.count)%r.cap] = be
		r.count++
	} else {
		// Overwrite the oldest entry.
		r.buf[r.head] = be
		r.head = (r.head + 1) % r.cap
	}
	return id
}

// Since returns all buffered events whose ID is strictly greater than lastID,
// in ascending ID order. Returns an empty slice when lastID is 0 or unknown.
func (r *Ring) Since(lastID int64) []BufferedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []BufferedEvent
	for i := 0; i < r.count; i++ {
		be := r.buf[(r.head+i)%r.cap]
		if be.ID > lastID {
			out = append(out, be)
		}
	}
	return out
}
