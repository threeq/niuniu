package service

import (
	"sync"
	"time"
)

// AccessCache is a simple TTL cache keyed by userID that stores AccessibleOwners.
// The TTL is 5 seconds. Concurrent access is safe via the embedded mutex.
type AccessCache struct {
	mu    sync.Mutex
	items map[int64]cacheEntry
	ttl   time.Duration
}

type cacheEntry struct {
	value     *AccessibleOwners
	expiresAt time.Time
}

// NewAccessCache returns an AccessCache with a 5-second TTL.
func NewAccessCache() *AccessCache {
	return &AccessCache{
		items: map[int64]cacheEntry{},
		ttl:   5 * time.Second,
	}
}

// Get returns the cached AccessibleOwners for userID and true on hit,
// or nil and false on miss or expiry.
func (c *AccessCache) Get(userID int64) (*AccessibleOwners, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[userID]
	if !ok || time.Now().After(entry.expiresAt) {
		delete(c.items, userID)
		return nil, false
	}
	return entry.value, true
}

// Put stores v in the cache for userID with the configured TTL.
func (c *AccessCache) Put(userID int64, v *AccessibleOwners) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[userID] = cacheEntry{
		value:     v,
		expiresAt: time.Now().Add(c.ttl),
	}
}

// Invalidate removes the cache entry for userID.
func (c *AccessCache) Invalidate(userID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, userID)
}

// InvalidateAll clears all entries from the cache.
func (c *AccessCache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = map[int64]cacheEntry{}
}
