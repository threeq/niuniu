package service

import (
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestWorkspaceDiffCache_SingleFlight pins the coalescing contract for the
// /workspaces/:id/diff recompute: the chat-panel badge, changes panel and
// content viewer all mount this query, and every diff.changed notification
// (one per debounced agent tool_result) invalidates it — while an agent is
// working, refetches stacked back-to-back 10s+ git diff + untracked scans
// that saturated the disk. Concurrent callers for one workspace must share a
// single compute.
func TestWorkspaceDiffCache_SingleFlight(t *testing.T) {
	c := newWsDiffCache(time.Minute)
	var computes atomic.Int64
	release := make(chan struct{})
	started := make(chan struct{})

	calls := 12
	var wg sync.WaitGroup
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := c.getOrCompute("1/true", func() wsDiffResult {
				if n := computes.Add(1); n == 1 {
					close(started)
				}
				<-release // hold the flight open so every caller piles up behind it
				return wsDiffResult{diffs: []RepoDiff{{Name: "repo"}}}
			})
			if r.err != nil || len(r.diffs) != 1 || r.diffs[0].Name != "repo" {
				t.Errorf("got %+v, want one repo group", r)
			}
		}()
	}

	<-started
	close(release)
	wg.Wait()

	if n := computes.Load(); n != 1 {
		t.Fatalf("compute ran %d times for %d concurrent callers; single-flight must collapse them to 1", n, calls)
	}
}

// TestWorkspaceDiffCache_ErrorsAreNotCached: a failed compute (DB hiccup,
// canceled request context) must reach its caller but never be stored — a
// cached error would serve 500s to healthy later requests for a full TTL.
func TestWorkspaceDiffCache_ErrorsAreNotCached(t *testing.T) {
	c := newWsDiffCache(time.Minute)
	boom := errors.New("boom")

	r := c.getOrCompute("1/false", func() wsDiffResult { return wsDiffResult{err: boom} })
	if !errors.Is(r.err, boom) {
		t.Fatalf("caller must see the compute error; got %v", r.err)
	}

	var computes atomic.Int64
	c.getOrCompute("1/false", func() wsDiffResult {
		computes.Add(1)
		return wsDiffResult{diffs: []RepoDiff{}}
	})
	if computes.Load() != 1 {
		t.Fatal("error result was cached — the next call must recompute instead of replaying the error")
	}
}

// TestWorkspaceDiffCache_PatchVariantsAreDistinctKeys: the summary (?patch
// absent) and runner-sync (?patch=1) shapes must not serve each other's data.
func TestWorkspaceDiffCache_PatchVariantsAreDistinctKeys(t *testing.T) {
	c := newWsDiffCache(time.Minute)
	a := c.getOrCompute("1/true", func() wsDiffResult { return wsDiffResult{diffs: []RepoDiff{{Name: "patched"}}} })
	b := c.getOrCompute("1/false", func() wsDiffResult { return wsDiffResult{diffs: []RepoDiff{{Name: "lean"}}} })
	if a.diffs[0].Name != "patched" || b.diffs[0].Name != "lean" {
		t.Fatalf("key collision: got %q / %q, want patched / lean", a.diffs[0].Name, b.diffs[0].Name)
	}
}

// TestWorkspaceDiffCache_InvalidateWorkspaceDropsBothVariants: git_ops
// commit/pull/push call this so the changes panel refreshes immediately after
// an explicit user git action instead of waiting out the TTL.
func TestWorkspaceDiffCache_InvalidateWorkspaceDropsBothVariants(t *testing.T) {
	c := newWsDiffCache(time.Minute)
	prefix := strconv.FormatInt(42, 10) + "/"
	compute := func() wsDiffResult { return wsDiffResult{diffs: []RepoDiff{{Name: "x"}}} }
	c.getOrCompute(prefix+"true", compute)
	c.getOrCompute(prefix+"false", compute)

	c.InvalidateWorkspace(42)

	var computes atomic.Int64
	counting := func() wsDiffResult { computes.Add(1); return wsDiffResult{} }
	c.getOrCompute(prefix+"true", counting)
	c.getOrCompute(prefix+"false", counting)
	if n := computes.Load(); n != 2 {
		t.Fatalf("after InvalidateWorkspace both variants recomputed %d times, want 2", n)
	}

	// Other workspaces must be untouched.
	if got := c.getOrCompute("43/false", func() wsDiffResult { return wsDiffResult{diffs: []RepoDiff{{Name: "other"}}} }); got.diffs[0].Name != "other" {
		t.Fatal("unexpected cross-workspace invalidation")
	}
}

// TestWorkspaceDiffCache_TTLExpiry bounds the recompute rate: within the TTL
// a second call is a cache hit, after it a recompute.
func TestWorkspaceDiffCache_TTLExpiry(t *testing.T) {
	c := newWsDiffCache(40 * time.Millisecond)
	var computes atomic.Int64
	compute := func() wsDiffResult { computes.Add(1); return wsDiffResult{} }
	c.getOrCompute("1/false", compute)
	c.getOrCompute("1/false", compute)
	if n := computes.Load(); n != 1 {
		t.Fatalf("second call inside TTL recomputed (computes=%d, want 1)", n)
	}
	time.Sleep(60 * time.Millisecond)
	c.getOrCompute("1/false", compute)
	if n := computes.Load(); n != 2 {
		t.Fatalf("call after TTL did not recompute (computes=%d, want 2)", n)
	}
}

// TestWorkspaceDiffCacheProductionTTL pins the production TTL. Each recompute
// runs git diff + untracked scan over the workspace's worktrees (10s+ on
// large repos on Windows); 15s plus single-flight bounds the churn to at most
// one flight per 15s per workspace instead of back-to-back stacking, while
// keeping the changes panel at most ~15s behind during ACTIVE agent editing
// (and instantly fresh on the next open once notifications stop).
func TestWorkspaceDiffCacheProductionTTL(t *testing.T) {
	if workspaceDiffCache.ttl != 15*time.Second {
		t.Fatalf("workspaceDiffCache.ttl = %v, want 15s", workspaceDiffCache.ttl)
	}
}
