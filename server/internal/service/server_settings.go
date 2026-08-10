package service

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// ServerSettingsService holds admin-tunable global K/V settings, cached for
// 5 seconds to avoid hitting the DB on every autohost stop decision.
type ServerSettingsService struct {
	db       *store.DB
	q        *store.Queries
	cacheTTL time.Duration

	mu    sync.RWMutex
	cache map[string]settingsCacheEntry
}

type settingsCacheEntry struct {
	value string
	at    time.Time
}

// NewServerSettingsService constructs a ServerSettingsService bound to the
// given *store.DB. The wrapper guarantees placeholder rewriting on Postgres;
// services that take *store.DB never need to call pgQ() by hand.
func NewServerSettingsService(db *store.DB) *ServerSettingsService {
	return &ServerSettingsService{
		db:       db,
		q:        db.Queries(),
		cacheTTL: 5 * time.Second,
		cache:    make(map[string]settingsCacheEntry),
	}
}

// SetCacheTTL is exposed for tests.
func (s *ServerSettingsService) SetCacheTTL(d time.Duration) {
	s.mu.Lock()
	s.cacheTTL = d
	s.cache = make(map[string]settingsCacheEntry) // wipe to force refresh
	s.mu.Unlock()
}

// GetInt returns the integer value for key, or def on miss/parse-error.
// Honors the 5s LRU cache.
func (s *ServerSettingsService) GetInt(ctx context.Context, key string, def int) int {
	v, ok := s.getCached(key)
	if !ok {
		v = s.loadFromDB(ctx, key)
		s.putCache(key, v)
	}
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// Put writes the value through and invalidates the cache for that key.
func (s *ServerSettingsService) Put(ctx context.Context, key, value string, updatedBy int64) error {
	err := s.q.UpsertServerSetting(ctx, store.UpsertServerSettingParams{
		Key:       key,
		Value:     value,
		UpdatedBy: sql.NullInt64{Int64: updatedBy, Valid: updatedBy > 0},
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.cache, key)
	s.mu.Unlock()
	return nil
}

// SeedIfAbsent writes value for key only if the key has never been set. Used at
// startup to materialize config defaults into the store so the admin UI and the
// guards read the same source. Idempotent; a present key (any value) is left
// untouched so user edits survive restarts.
func (s *ServerSettingsService) SeedIfAbsent(ctx context.Context, key, value string) error {
	if _, err := s.q.GetServerSetting(ctx, key); err == nil {
		return nil // already set — do not clobber a user edit
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return s.q.UpsertServerSetting(ctx, store.UpsertServerSettingParams{
		Key:       key,
		Value:     value,
		UpdatedBy: sql.NullInt64{}, // system seed, no user
	})
}

func (s *ServerSettingsService) getCached(key string) (string, bool) {
	s.mu.RLock()
	e, ok := s.cache[key]
	ttl := s.cacheTTL
	s.mu.RUnlock()
	if !ok {
		return "", false
	}
	if time.Since(e.at) > ttl {
		return "", false
	}
	return e.value, true
}

func (s *ServerSettingsService) putCache(key, value string) {
	s.mu.Lock()
	s.cache[key] = settingsCacheEntry{value: value, at: time.Now()}
	s.mu.Unlock()
}

func (s *ServerSettingsService) loadFromDB(ctx context.Context, key string) string {
	v, err := s.q.GetServerSetting(ctx, key)
	if err != nil {
		return ""
	}
	return v
}
