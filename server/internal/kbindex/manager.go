package kbindex

import (
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
)

// Manager owns the lifecycle of KBIndex instances and hides the driver choice.
//
//   - On SQLite it opens one per-owner sidecar (kb_index.db) per distinct path
//     and caches it, so repeated lookups for the same owner reuse a single
//     connection pool.
//   - On Postgres there is a single shared index (one kb_chunks table scoped by
//     kb_id) reused for every owner; the sidecar path argument is ignored.
type Manager struct {
	driver string

	mu     sync.Mutex
	sqlite map[string]*SQLiteIndex // keyed by sidecar path
	shared KBIndex                 // Postgres-backed, lazily/eagerly set
	closed bool
}

// NewManager constructs a Manager for the given storage driver. pg must be the
// main Postgres *sql.DB when driver == "postgres"; it is ignored otherwise.
func NewManager(driver string, pg *sql.DB) *Manager {
	m := &Manager{driver: driver, sqlite: make(map[string]*SQLiteIndex)}
	if driver == "postgres" && pg != nil {
		if idx, err := NewPostgresIndex(pg); err == nil {
			m.shared = idx
		} else {
			// Don't swallow: a failed schema/pg_trgm setup leaves m.shared nil and
			// every later Get returns the generic "not initialized" error, hiding
			// the real cause. Log it so the root failure is recoverable.
			slog.Error("kbindex: postgres index init failed; KB search/index will be unavailable", "err", err)
		}
	}
	return m
}

// Get returns the KBIndex for the given sidecar path. For Postgres the path is
// ignored and the shared index is returned.
func (m *Manager) Get(sidecarPath string) (KBIndex, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, fmt.Errorf("kbindex: manager closed")
	}
	if m.driver == "postgres" {
		if m.shared == nil {
			return nil, fmt.Errorf("kbindex: postgres index not initialized")
		}
		return m.shared, nil
	}
	if idx, ok := m.sqlite[sidecarPath]; ok {
		return idx, nil
	}
	idx, err := OpenSQLiteIndex(sidecarPath)
	if err != nil {
		return nil, err
	}
	m.sqlite[sidecarPath] = idx
	return idx, nil
}

// Close closes every owned index. The Postgres shared index's Close is a no-op
// (the caller owns that *sql.DB).
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	var firstErr error
	for path, idx := range m.sqlite {
		if err := idx.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(m.sqlite, path)
	}
	if m.shared != nil {
		if err := m.shared.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
