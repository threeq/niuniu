package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// newTestRelayHandler constructs a RelayHandler with no service attached and
// a sane default throttle interval. Tests that need a specific interval or a
// pre-populated throttle map adjust the fields directly.
func newTestRelayHandler(interval time.Duration) *RelayHandler {
	return &RelayHandler{
		verifyEmailLast:     make(map[string]time.Time),
		verifyEmailInterval: interval,
	}
}

// TestRequestEmailVerification_Throttle423 verifies the HTTP response when a
// caller hits the throttle: 429 + populated Retry-After header + JSON body
// with error=rate_limited and numeric retry_after.
func TestRequestEmailVerification_Throttle429(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := newTestRelayHandler(time.Minute)
	h.verifyEmailLast["default"] = time.Now()

	r := gin.New()
	r.POST("/api/relay/verify-email/request", h.RequestEmailVerification)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/relay/verify-email/request", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d (body=%s)", w.Code, w.Body.String())
	}

	retryAfter := w.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Fatalf("expected Retry-After header to be set")
	}
	n, err := strconv.Atoi(retryAfter)
	if err != nil || n <= 0 || n > 61 {
		t.Fatalf("Retry-After should be a positive int ≤ 61, got %q", retryAfter)
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["error"] != "rate_limited" {
		t.Fatalf("expected error=rate_limited, got %v", body["error"])
	}
	if _, ok := body["retry_after"].(float64); !ok {
		t.Fatalf("expected numeric retry_after in body, got %v", body["retry_after"])
	}
}

// TestTryReserveVerifyEmail_GatePasses verifies the first call for a scope is
// admitted and the timestamp is recorded.
func TestTryReserveVerifyEmail_GatePasses(t *testing.T) {
	h := newTestRelayHandler(time.Minute)
	now := time.Now()
	if retry, ok := h.tryReserveVerifyEmail("s1", now); !ok {
		t.Fatalf("first call must pass, got retry=%d ok=false", retry)
	}
	h.verifyEmailMu.Lock()
	defer h.verifyEmailMu.Unlock()
	if ts, ok := h.verifyEmailLast["s1"]; !ok || !ts.Equal(now) {
		t.Fatalf("timestamp not recorded: have=%v ok=%v want=%v", ts, ok, now)
	}
}

// TestTryReserveVerifyEmail_Blocks checks that a second call within the
// window is refused and returns a positive retry-after.
func TestTryReserveVerifyEmail_Blocks(t *testing.T) {
	h := newTestRelayHandler(time.Minute)
	base := time.Now()
	if _, ok := h.tryReserveVerifyEmail("s1", base); !ok {
		t.Fatalf("first call must pass")
	}
	retry, ok := h.tryReserveVerifyEmail("s1", base.Add(5*time.Second))
	if ok {
		t.Fatalf("second call within window must be blocked")
	}
	if retry <= 0 || retry > 61 {
		t.Fatalf("retry-after should be in (0, 61], got %d", retry)
	}
}

// TestTryReserveVerifyEmail_AllowsAfterExpiry verifies an entry older than
// verifyEmailInterval no longer blocks.
func TestTryReserveVerifyEmail_AllowsAfterExpiry(t *testing.T) {
	h := newTestRelayHandler(time.Minute)
	h.verifyEmailLast["s1"] = time.Now().Add(-2 * time.Minute)
	if _, ok := h.tryReserveVerifyEmail("s1", time.Now()); !ok {
		t.Fatalf("expired entry must not block")
	}
}

// TestTryReserveVerifyEmail_PerScope confirms each scope has its own budget.
func TestTryReserveVerifyEmail_PerScope(t *testing.T) {
	h := newTestRelayHandler(time.Minute)
	now := time.Now()
	if _, ok := h.tryReserveVerifyEmail("alice", now); !ok {
		t.Fatalf("alice first call must pass")
	}
	if _, ok := h.tryReserveVerifyEmail("bob", now); !ok {
		t.Fatalf("bob first call must pass (different scope)")
	}
	if _, ok := h.tryReserveVerifyEmail("alice", now); ok {
		t.Fatalf("alice second call must be blocked")
	}
}

// TestTryReserveVerifyEmail_TOCTOU stresses the atomic reserve path: many
// goroutines hit the gate concurrently, exactly one must pass. The old
// sync.Map Load-then-Store pattern would let multiple callers through.
func TestTryReserveVerifyEmail_TOCTOU(t *testing.T) {
	h := newTestRelayHandler(time.Minute)

	const n = 100
	var passed int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	now := time.Now()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, ok := h.tryReserveVerifyEmail("shared", now); ok {
				atomic.AddInt32(&passed, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := atomic.LoadInt32(&passed); got != 1 {
		t.Fatalf("TOCTOU: expected exactly 1 caller to pass, got %d", got)
	}
}

// TestReleaseVerifyEmail_RollsBackOnFailure confirms a failed downstream call
// clears the reservation, so the next caller doesn't have to wait out the
// throttle for a call that never actually went through.
func TestReleaseVerifyEmail_RollsBackOnFailure(t *testing.T) {
	h := newTestRelayHandler(time.Minute)
	now := time.Now()
	if _, ok := h.tryReserveVerifyEmail("s1", now); !ok {
		t.Fatalf("reserve: first call must pass")
	}
	h.releaseVerifyEmail("s1", now)
	if _, ok := h.tryReserveVerifyEmail("s1", now.Add(time.Millisecond)); !ok {
		t.Fatalf("after release, next call must pass")
	}
}

// TestReleaseVerifyEmail_DoesNotOverrideNewer verifies a late rollback from a
// failed call does NOT delete a newer reservation stored after the original
// call already surrendered its slot (edge case where the failed caller's
// release arrives after the throttle window has elapsed AND a new caller has
// already reserved again).
func TestReleaseVerifyEmail_DoesNotOverrideNewer(t *testing.T) {
	h := newTestRelayHandler(time.Minute)
	original := time.Now()
	h.verifyEmailLast["s1"] = original

	// Simulate a newer successful reservation replacing the original.
	newer := original.Add(2 * time.Minute)
	h.verifyEmailLast["s1"] = newer

	// Late release for the original should be a no-op, preserving `newer`.
	h.releaseVerifyEmail("s1", original)

	h.verifyEmailMu.Lock()
	defer h.verifyEmailMu.Unlock()
	if ts, ok := h.verifyEmailLast["s1"]; !ok || !ts.Equal(newer) {
		t.Fatalf("expected newer reservation to be preserved, have=%v ok=%v", ts, ok)
	}
}
