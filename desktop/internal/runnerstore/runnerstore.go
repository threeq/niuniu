// Package runnerstore is the desktop-side registry of local execution Runners
// (#526·子C). Each entry records one workspace's binding of a REMOTE connection
// to a directory on this machine: which server it is connected to, the bound
// local directory, the current run status, recent activity and a bounded log
// tail.
//
// It is the single desktop-owned source the global "本地 Runner 管理" window
// reads and mutates, aggregating Runners across every remote connection. In the
// 方案 A phase the backend Runner (real process, long connection, tool injection)
// is stubbed — start/stop/delete here mutate registry state and persist it — so
// 子 B can later drive the same records from the live reverse channel.
//
// The store is safe for concurrent use (Wails message-processor goroutines call
// the bindings while connection event/close hooks may register/unregister).
// State is persisted to a JSON file with an atomic write, mirroring
// internal/config.
package runnerstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Status values for a Runner. Mirrors the SPA per-workspace state machine
// (#526·子A) plus a "stopped" state the global manager can put a Runner into.
const (
	StatusActive     = "active"
	StatusStopped    = "stopped"
	StatusConnecting = "connecting"
	StatusError      = "error"
)

// maxLogEntries bounds the per-runner log tail kept in memory / on disk so a
// long-lived Runner can't grow the registry file unbounded.
const maxLogEntries = 1000

// LogEntry is one line of a Runner's execution log (command / stdout / stderr /
// system). ts is unix milliseconds.
type LogEntry struct {
	TS    int64  `json:"ts"`
	Level string `json:"level"`
	Text  string `json:"text"`
}

// Runner is one registered local Runner binding. JSON tags are snake_case
// because the struct is marshalled straight to the management frontend by Wails.
type Runner struct {
	ID             string `json:"id"`
	ConnectionKey  string `json:"connection_key"`  // "host:port" of the connected server
	ConnectionName string `json:"connection_name"` // node display name
	WorkspaceID    string `json:"workspace_id"`
	WorkspaceName  string `json:"workspace_name"`
	LocalDir       string `json:"local_dir"`
	Status         string `json:"status"`
	LastActivityMS int64  `json:"last_activity_ms"`
	LastLogSummary string `json:"last_log_summary"`
	CreatedMS      int64  `json:"created_ms"`
}

// persistState is the on-disk shape: the runner records plus their log tails
// kept out of the Runner struct so List() payloads stay small.
type persistState struct {
	Runners []Runner              `json:"runners"`
	Logs    map[string][]LogEntry `json:"logs,omitempty"`
}

// Store is the concurrency-safe registry.
type Store struct {
	mu      sync.Mutex
	path    string
	runners []Runner
	logs    map[string][]LogEntry
	seq     int64

	// now/idSeed are overridable in tests; production uses wall clock.
	now func() time.Time
}

// New creates a store backed by path. Call Load once after construction to
// hydrate from disk; a missing/corrupt file yields an empty store.
func New(path string) *Store {
	return &Store{
		path: path,
		logs: make(map[string][]LogEntry),
		now:  time.Now,
	}
}

// Load reads the persisted state. A missing file is not an error (fresh store);
// a corrupt file is tolerated by starting empty so a bad write never bricks the
// manager.
func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var st persistState
	if err := json.Unmarshal(data, &st); err != nil {
		// Corrupt file — start clean rather than fail the whole window.
		s.runners = nil
		s.logs = make(map[string][]LogEntry)
		return nil
	}
	s.runners = st.Runners
	if st.Logs != nil {
		s.logs = st.Logs
	} else {
		s.logs = make(map[string][]LogEntry)
	}
	return nil
}

// save writes state atomically. Caller must hold s.mu. A persistence failure is
// returned to the caller but never mutates in-memory state.
func (s *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	st := persistState{Runners: s.runners, Logs: s.logs}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) nowMS() int64 { return s.now().UnixMilli() }

// nextID mints a process-unique id. Caller must hold s.mu.
func (s *Store) nextID() string {
	s.seq++
	return "runner-" + strconv.FormatInt(s.now().UnixNano(), 10) + "-" + strconv.FormatInt(s.seq, 10)
}

// findIndexByKey returns the index of the runner bound to (connKey, workspaceID),
// or -1. Caller must hold s.mu. This pair is the natural identity of a binding:
// one Runner per workspace per connected server.
func (s *Store) findIndexByKey(connKey, workspaceID string) int {
	for i := range s.runners {
		if s.runners[i].ConnectionKey == connKey && s.runners[i].WorkspaceID == workspaceID {
			return i
		}
	}
	return -1
}

func (s *Store) findIndexByID(id string) int {
	for i := range s.runners {
		if s.runners[i].ID == id {
			return i
		}
	}
	return -1
}

// List returns a copy of all registered runners (without their log tails).
func (s *Store) List() []Runner {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Runner, len(s.runners))
	copy(out, s.runners)
	return out
}

// Get returns the runner with id and whether it was found.
func (s *Store) Get(id string) (Runner, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.findIndexByID(id); i >= 0 {
		return s.runners[i], true
	}
	return Runner{}, false
}

// Upsert registers or updates the Runner for (ConnectionKey, WorkspaceID). On a
// new binding it assigns an ID + CreatedMS and defaults Status to active. On an
// existing binding it updates the mutable fields but preserves ID/CreatedMS.
// Returns the stored record.
func (s *Store) Upsert(r Runner) (Runner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowMS()
	if i := s.findIndexByKey(r.ConnectionKey, r.WorkspaceID); i >= 0 {
		existing := s.runners[i]
		existing.ConnectionName = r.ConnectionName
		existing.WorkspaceName = r.WorkspaceName
		existing.LocalDir = r.LocalDir
		if r.Status != "" {
			existing.Status = r.Status
		}
		existing.LastActivityMS = now
		s.runners[i] = existing
		if err := s.save(); err != nil {
			return existing, err
		}
		return existing, nil
	}
	r.ID = s.nextID()
	r.CreatedMS = now
	r.LastActivityMS = now
	if r.Status == "" {
		r.Status = StatusActive
	}
	s.runners = append(s.runners, r)
	if err := s.save(); err != nil {
		return r, err
	}
	return r, nil
}

// Remove deletes the runner with id (and its logs). Returns true if one was
// removed. This is the "解绑" primitive: the binding record goes away entirely.
func (s *Store) Remove(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.findIndexByID(id)
	if i < 0 {
		return false, nil
	}
	s.runners = append(s.runners[:i], s.runners[i+1:]...)
	delete(s.logs, id)
	return true, s.save()
}

// SetStatus updates a runner's status and bumps its last-activity time. Returns
// true if the runner exists.
func (s *Store) SetStatus(id, status string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.findIndexByID(id)
	if i < 0 {
		return false, nil
	}
	s.runners[i].Status = status
	s.runners[i].LastActivityMS = s.nowMS()
	return true, s.save()
}

// AppendLog adds a log line to a runner, updating its summary + activity time.
// The tail is bounded to maxLogEntries. No-op (false) for an unknown id.
func (s *Store) AppendLog(id string, e LogEntry) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.findIndexByID(id)
	if i < 0 {
		return false, nil
	}
	if e.TS == 0 {
		e.TS = s.nowMS()
	}
	tail := append(s.logs[id], e)
	if len(tail) > maxLogEntries {
		tail = tail[len(tail)-maxLogEntries:]
	}
	s.logs[id] = tail
	s.runners[i].LastLogSummary = e.Text
	s.runners[i].LastActivityMS = e.TS
	return true, s.save()
}

// Logs returns a copy of the runner's log tail (nil for unknown id).
func (s *Store) Logs(id string) []LogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.logs[id]
	out := make([]LogEntry, len(src))
	copy(out, src)
	return out
}
