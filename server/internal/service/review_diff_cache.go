package service

import (
	"strconv"
	"sync"
	"time"
)

// wsDiffResult is one workspace-diff computation: the per-repo groups plus
// the error, kept together so a failed compute can flow through the flight to
// its waiters without ever being cached.
type wsDiffResult struct {
	diffs []RepoDiff
	err   error
}

type wsDiffEntry struct {
	result   wsDiffResult
	computed time.Time
}

// wsDiffFlight is one in-flight workspace-diff compute shared by all callers
// waiting on the same key. result is written before done is closed; waiters
// read it after <-done (close gives the happens-before edge).
type wsDiffFlight struct {
	done   chan struct{}
	result wsDiffResult
}

// wsDiffCache memoizes GET /workspaces/:id/diff recomputes under a TTL and
// coalesces concurrent recomputes of the same workspace into one flight.
//
// Why both: the chat-panel badge, changes panel and content viewer all mount
// this query, and every diff.changed notification (one per debounced agent
// tool_result) invalidates it. Each recompute runs git diff + untracked scan
// over the workspace's worktrees — 10s+ on large repos on Windows — so
// invalidation-driven refetches used to stack back-to-back fans that
// saturated the disk. The TTL bounds the recompute rate; the flight bounds
// the concurrency. Failed computes reach their caller and current waiters but
// are never stored (a cached error would serve 500s to healthy requests for a
// full TTL).
type wsDiffCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]wsDiffEntry
	flights map[string]*wsDiffFlight
}

func newWsDiffCache(ttl time.Duration) *wsDiffCache {
	return &wsDiffCache{
		ttl:     ttl,
		entries: make(map[string]wsDiffEntry),
		flights: make(map[string]*wsDiffFlight),
	}
}

// wsDiffCacheKey identifies one diff shape: the workspace and whether the
// runner-sync variant (?patch=1, full raw_patch bytes) was requested.
func wsDiffCacheKey(workspaceID int64, keepPatch bool) string {
	return strconv.FormatInt(workspaceID, 10) + "/" + strconv.FormatBool(keepPatch)
}

// getOrCompute returns the cached result for key when fresh; otherwise runs
// compute exactly once per key regardless of concurrent callers, hands the
// result to every waiter, and stores it only when compute succeeded.
func (c *wsDiffCache) getOrCompute(key string, compute func() wsDiffResult) wsDiffResult {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && time.Since(e.computed) <= c.ttl {
		c.mu.Unlock()
		return e.result
	}
	if f, ok := c.flights[key]; ok {
		c.mu.Unlock()
		<-f.done
		return f.result
	}
	f := &wsDiffFlight{done: make(chan struct{})}
	c.flights[key] = f
	c.mu.Unlock()

	// Panic guard: gin recovers the panicking request, but the flight must not
	// stay registered forever — that would deadlock every later caller on this
	// key. Release waiters with the zero value and drop the flight instead.
	completed := false
	defer func() {
		if !completed {
			c.mu.Lock()
			delete(c.flights, key)
			c.mu.Unlock()
			close(f.done)
		}
	}()

	result := compute()

	c.mu.Lock()
	if result.err == nil {
		c.entries[key] = wsDiffEntry{result: result, computed: time.Now()}
	}
	delete(c.flights, key)
	c.mu.Unlock()

	f.result = result
	close(f.done)
	completed = true
	return result
}

// InvalidateWorkspace drops both shape variants (summary + runner sync) for a
// workspace so the changes panel refreshes immediately after explicit user
// git actions (commit/pull/push) instead of waiting out the TTL.
func (c *wsDiffCache) InvalidateWorkspace(workspaceID int64) {
	prefix := strconv.FormatInt(workspaceID, 10) + "/"
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.entries {
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			delete(c.entries, key)
		}
	}
}

// InvalidateAll drops every entry. Test hygiene only: service tests get a
// fresh SQLite file per test, so workspace IDs repeat across tests and a
// warm entry from one test would bleed into the next.
func (c *wsDiffCache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]wsDiffEntry)
}

// workspaceDiffCacheTTL bounds the /workspaces/:id/diff recompute rate: at
// most one flight per workspace per 15s. During ACTIVE agent editing the
// changes panel lags at most ~15s (any diff view is racy then anyway); once
// notifications stop, the next open recomputes fresh.
const workspaceDiffCacheTTL = 15 * time.Second

// workspaceDiffCache is the process-wide diff cache; keys are globally
// unique workspace IDs, so a package-level instance is safe (same reasoning
// as sidebarGitCache).
var workspaceDiffCache = newWsDiffCache(workspaceDiffCacheTTL)
