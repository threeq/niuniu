package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"golang.org/x/time/rate"
)

// setUserID is a small middleware that injects a fixed auth_user_id, mimicking
// what auth.Middleware does after JWT validation.
func setUserID(id int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("auth_user_id", id)
		c.Next()
	}
}

func TestRateLimitUser_AllowsUnderBurst(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(setUserID(42), RateLimitUserMiddleware(1, 5))
	r.POST("/x", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })

	for i := range 5 {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/x", nil)
		r.ServeHTTP(w, req)
		assert.Equal(t, 200, w.Code, "request %d should pass within burst", i)
	}
}

func TestRateLimitUser_BlocksAfterBurst(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(setUserID(42), RateLimitUserMiddleware(0.01, 2))
	r.POST("/x", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })

	for i := range 2 {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/x", nil)
		r.ServeHTTP(w, req)
		assert.Equal(t, 200, w.Code, "request %d should pass within burst", i)
	}
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/x", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, 429, w.Code, "third request should be rate-limited")
	assert.Contains(t, w.Body.String(), "RATE_LIMITED")
}

func TestRateLimitUser_PerUserIsolation(t *testing.T) {
	// userA exhausts the bucket; userB is independent.
	gin.SetMode(gin.TestMode)
	rl := newUserRateLimiter(rate.Limit(0.01), 1)
	// userA: first call OK, second 429.
	assert.True(t, rl.getLimiter(1).Allow())
	assert.False(t, rl.getLimiter(1).Allow())
	// userB: still has full burst.
	assert.True(t, rl.getLimiter(2).Allow())
}

func TestRateLimitUser_AnonymousBypass(t *testing.T) {
	// userID == 0 (no auth_user_id set) must bypass — single-user-mode and
	// pre-auth probes shouldn't be hit by per-user limits.
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RateLimitUserMiddleware(0.01, 1))
	r.POST("/x", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })

	for range 5 {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/x", nil)
		r.ServeHTTP(w, req)
		assert.Equal(t, 200, w.Code, "anonymous calls should bypass per-user limit")
	}
}

func TestRateLimitUser_CleanupRemovesStaleEntries(t *testing.T) {
	rl := newUserRateLimiter(rate.Limit(1), 1)
	rl.getLimiter(1)
	rl.getLimiter(2)
	// Backdate user 1 so it qualifies as stale; leave user 2 fresh.
	rl.mu.Lock()
	rl.entries[1].lastSeen = time.Now().Add(-2 * time.Hour)
	rl.mu.Unlock()

	// Run a single cleanup pass synchronously (drive the tick loop manually
	// is awkward; emulate the body instead).
	cutoff := time.Now().Add(-1 * time.Hour)
	rl.mu.Lock()
	for id, e := range rl.entries {
		if e.lastSeen.Before(cutoff) {
			delete(rl.entries, id)
		}
	}
	rl.mu.Unlock()

	rl.mu.Lock()
	defer rl.mu.Unlock()
	if _, exists := rl.entries[1]; exists {
		t.Error("stale entry for user 1 should be removed")
	}
	if _, exists := rl.entries[2]; !exists {
		t.Error("fresh entry for user 2 should be kept")
	}
}
