package service

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestWorktreeGitCache_SingleFlight pins the coalescing contract: concurrent
// getOrCompute calls for the same worktree+base must share ONE compute.
//
// Production incident (measured on the user's machine): every agent
// tool_result (debounced 500ms) broadcasts git_status.changed, the frontend
// invalidates BOTH sidebar-git scope variants, and each sidebar-git request
// fans git status+rev-list across every worktree of every workspace —
// 50–125s per pass on Windows. Without single-flight those requests STACKED
// (2 variants × repeated invalidations), permanently saturating the disk and
// making every other query — message flow included — crawl.
func TestWorktreeGitCache_SingleFlight(t *testing.T) {
	c := newWorktreeGitCache(time.Minute)
	var computes atomic.Int64
	release := make(chan struct{})
	started := make(chan struct{})

	calls := 16
	var wg sync.WaitGroup
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got := c.getOrCompute("wt", "main", func() worktreeGitInfo {
				if n := computes.Add(1); n == 1 {
					close(started)
				}
				<-release // hold the flight open so every caller piles up behind it
				return worktreeGitInfo{changesCount: 7, aheadCount: 3}
			})
			if got.changesCount != 7 || got.aheadCount != 3 {
				t.Errorf("got %+v, want {7 3}", got)
			}
		}()
	}

	<-started
	// Every caller is now either in the flight or waiting on it; release.
	close(release)
	wg.Wait()

	if n := computes.Load(); n != 1 {
		t.Fatalf("compute ran %d times for %d concurrent callers; single-flight must collapse them to 1", n, calls)
	}
}

// TestWorktreeGitCache_SingleFlight_DifferentBaseBranches computes
// independently: the ahead count depends on baseBranch, so callers with
// different bases must NOT share one flight (they'd get each other's numbers).
func TestWorktreeGitCache_SingleFlight_DifferentBaseBranches(t *testing.T) {
	c := newWorktreeGitCache(time.Minute)
	var computes atomic.Int64
	release := make(chan struct{})

	var wg sync.WaitGroup
	for _, base := range []string{"main", "develop"} {
		wg.Add(1)
		go func(base string) {
			defer wg.Done()
			got := c.getOrCompute("wt", base, func() worktreeGitInfo {
				computes.Add(1)
				<-release
				return worktreeGitInfo{aheadCount: len(base)}
			})
			if got.aheadCount != len(base) {
				t.Errorf("base %q: aheadCount = %d, want %d", base, got.aheadCount, len(base))
			}
		}(base)
	}
	time.Sleep(50 * time.Millisecond) // let both flights register
	close(release)
	wg.Wait()

	if n := computes.Load(); n != 2 {
		t.Fatalf("compute ran %d times, want 2 (one per distinct base branch)", n)
	}
}

// TestWorktreeGitCache_TTLWindowServesCachedResult: within the TTL a second
// call is a cache hit (no recompute); after the TTL it recomputes.
func TestWorktreeGitCache_TTLWindowServesCachedResult(t *testing.T) {
	c := newWorktreeGitCache(80 * time.Millisecond)
	var computes atomic.Int64
	compute := func() worktreeGitInfo {
		computes.Add(1)
		return worktreeGitInfo{changesCount: 1}
	}

	c.getOrCompute("wt", "main", compute)
	c.getOrCompute("wt", "main", compute)
	if n := computes.Load(); n != 1 {
		t.Fatalf("second call inside TTL recomputed (computes=%d, want 1)", n)
	}

	time.Sleep(120 * time.Millisecond)
	c.getOrCompute("wt", "main", compute)
	if n := computes.Load(); n != 2 {
		t.Fatalf("call after TTL did not recompute (computes=%d, want 2)", n)
	}
}

// TestSidebarGitCacheProductionTTL pins the production TTL. The original 5s
// was shorter than a single full fan-out (measured 50–125s), so the cache
// NEVER survived between requests — every invalidated refetch was another
// complete git storm. 60s bounds the recompute rate while Invalidate() still
// gives instant freshness after explicit user mutations (commit/discard).
func TestSidebarGitCacheProductionTTL(t *testing.T) {
	if sidebarGitCache.ttl != 60*time.Second {
		t.Fatalf("sidebarGitCache.ttl = %v, want 60s", sidebarGitCache.ttl)
	}
}

// TestWorktreeGitCache_InvalidateForcesRecompute: an explicit Invalidate
// must override a still-warm TTL entry (git_ops.go relies on this for
// instant badge freshness after commits/discards).
func TestWorktreeGitCache_InvalidateForcesRecompute(t *testing.T) {
	c := newWorktreeGitCache(time.Hour)
	var computes atomic.Int64
	compute := func() worktreeGitInfo {
		computes.Add(1)
		return worktreeGitInfo{changesCount: 2}
	}

	c.getOrCompute("wt", "main", compute)
	c.Invalidate("wt")
	c.getOrCompute("wt", "main", compute)
	if n := computes.Load(); n != 2 {
		t.Fatalf(" Invalidate + getOrCompute recomputed %d times, want 2", n)
	}
}
