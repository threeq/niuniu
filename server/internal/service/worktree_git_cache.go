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

// worktreeGitFlight is one in-progress compute shared by all callers waiting
// on the same (path, baseBranch) key.
type worktreeGitFlight struct {
	done chan struct{}
	info worktreeGitInfo
}

// worktreeGitCache memoizes the per-worktree git subprocess work that the
// sidebar recomputes on every git_status notification. With hundreds or
// thousands of workspaces, each refresh would otherwise spawn thousands of
// short-lived git processes (especially expensive on Windows). A TTL keeps the
// change/ahead badges near-real-time while collapsing repeated polls to O(1).
type worktreeGitCache struct {
	mu    sync.Mutex
	ttl   time.Duration
	items map[string]worktreeGitCacheEntry
	// flights dedupes CONCURRENT computes per (path, baseBranch). Without it,
	// overlapping requests (the sidebar fires two scope variants, and agents'
	// tool_results invalidate faster than one full fan-out completes) each
	// launch their own git subprocess storm instead of sharing one.
	flights map[string]*worktreeGitFlight
}

func newWorktreeGitCache(ttl time.Duration) *worktreeGitCache {
	return &worktreeGitCache{ttl: ttl, items: make(map[string]worktreeGitCacheEntry), flights: make(map[string]*worktreeGitFlight)}
}

// set stores freshly computed git info for a worktree path.
func (c *worktreeGitCache) set(path string, info worktreeGitInfo, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[path] = worktreeGitCacheEntry{info: info, computed: now}
}

// getOrCompute serves the cached entry when fresh; otherwise runs compute
// exactly ONCE per (path, baseBranch) no matter how many callers arrive while
// it is in flight (the rest wait on the shared flight). The TTL check and the
// flight lookup share ONE critical section: checking them under separate locks
// left a window where a caller could miss the (still-empty) cache, then find
// the flight already retired and start a duplicate compute.
// baseBranch joins the flight key because aheadCount depends on it, but the
// TTL entry stays keyed by path alone (existing semantics: the badge is
// approximate).
func (c *worktreeGitCache) getOrCompute(path, baseBranch string, compute func() worktreeGitInfo) worktreeGitInfo {
	key := path + "\x00" + baseBranch

	c.mu.Lock()
	if e, ok := c.items[path]; ok && time.Since(e.computed) <= c.ttl {
		c.mu.Unlock()
		return e.info
	}
	if f, ok := c.flights[key]; ok {
		c.mu.Unlock()
		<-f.done
		return f.info
	}
	f := &worktreeGitFlight{done: make(chan struct{})}
	c.flights[key] = f
	c.mu.Unlock()

	// Panic guard: gin recovers the panicking request, but a flight left
	// registered forever would deadlock every later caller on this key.
	completed := false
	defer func() {
		if !completed {
			c.mu.Lock()
			delete(c.flights, key)
			c.mu.Unlock()
			close(f.done)
		}
	}()

	f.info = compute()
	c.set(path, f.info, time.Now())
	close(f.done)
	completed = true

	c.mu.Lock()
	delete(c.flights, key)
	c.mu.Unlock()
	return f.info
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
// and avoids threading the cache through DI.
//
// TTL is 60s (was 5s): one full fan-out was MEASURED at 50–125s on Windows
// (~40 worktrees × git status + rev-list), so a 5s TTL never survived between
// requests — every git_status-triggered refetch was a complete recompute and
// the overlapping fans permanently saturated the disk (the "switching
// workspaces makes everything crawl" incident). 60s bounds recompute to at
// most one pass per minute; git_ops.go still calls Invalidate() for instant
// freshness after explicit user mutations (commit/discard).
var sidebarGitCache = newWorktreeGitCache(60 * time.Second)
