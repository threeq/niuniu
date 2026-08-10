package service

import (
	"sync"
	"time"
)

// worktreeGitInfo is the git-derived sidebar data for a single worktree: the
// number of working-tree changes and how many commits it is ahead of its base
// branch. Both are computed by spawning git subprocesses (git status +
// rev-list), which is the dominant cost of building the workspace sidebar.
type worktreeGitInfo struct {
	changesCount int
	aheadCount   int
}

type worktreeGitCacheEntry struct {
	info     worktreeGitInfo
	computed time.Time
}

// worktreeGitCache memoizes the per-worktree git subprocess work that the
// sidebar recomputes on every GET /api/workspaces. With hundreds or thousands
// of workspaces, each refresh would otherwise spawn thousands of short-lived
// git processes (especially expensive on Windows). A short TTL keeps the
// change/ahead badges near-real-time while collapsing repeated polls to O(1).
type worktreeGitCache struct {
	mu    sync.Mutex
	ttl   time.Duration
	items map[string]worktreeGitCacheEntry
}

func newWorktreeGitCache(ttl time.Duration) *worktreeGitCache {
	return &worktreeGitCache{ttl: ttl, items: make(map[string]worktreeGitCacheEntry)}
}

// get returns the cached info for a worktree path when still within the TTL.
func (c *worktreeGitCache) get(path string, now time.Time) (worktreeGitInfo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[path]
	if !ok || now.Sub(e.computed) > c.ttl {
		return worktreeGitInfo{}, false
	}
	return e.info, true
}

// set stores freshly computed git info for a worktree path.
func (c *worktreeGitCache) set(path string, info worktreeGitInfo, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[path] = worktreeGitCacheEntry{info: info, computed: now}
}

// Invalidate drops a worktree's cached git info so the next sidebar build
// recomputes it immediately (call after operations that mutate the working
// tree, e.g. commits or discards, when instant feedback is desired). The TTL
// already self-heals staleness, so this is an optional freshness optimization.
func (c *worktreeGitCache) Invalidate(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, path)
}

// sidebarGitCache is the process-wide cache for sidebar git badges. Keyed by
// absolute worktree path (globally unique), so a package-level instance is safe
// and avoids threading the cache through DI. TTL is deliberately short: the
// badges are approximate and refreshed on the next poll.
var sidebarGitCache = newWorktreeGitCache(5 * time.Second)
