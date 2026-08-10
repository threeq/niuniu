package auth

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

type userEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type userRateLimiter struct {
	mu      sync.Mutex
	entries map[int64]*userEntry
	rate    rate.Limit
	burst   int
	done    chan struct{}
}

func newUserRateLimiter(r rate.Limit, b int) *userRateLimiter {
	rl := &userRateLimiter{
		entries: make(map[int64]*userEntry),
		rate:    r,
		burst:   b,
		done:    make(chan struct{}),
	}
	go rl.cleanup(10*time.Minute, 1*time.Hour)
	return rl
}

func (rl *userRateLimiter) getLimiter(userID int64) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	entry, exists := rl.entries[userID]
	if !exists {
		entry = &userEntry{limiter: rate.NewLimiter(rl.rate, rl.burst)}
		rl.entries[userID] = entry
	}
	entry.lastSeen = time.Now()
	return entry.limiter
}

func (rl *userRateLimiter) cleanup(interval, maxAge time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			rl.mu.Lock()
			cutoff := time.Now().Add(-maxAge)
			for id, entry := range rl.entries {
				if entry.lastSeen.Before(cutoff) {
					delete(rl.entries, id)
				}
			}
			rl.mu.Unlock()
		case <-rl.done:
			return
		}
	}
}

// RateLimitUserMiddleware returns a per-user token-bucket rate limiter keyed
// by `auth_user_id` (set by Auth.Middleware). Anonymous requests (userID == 0)
// bypass the limit so paths that legitimately predate auth — single-user
// mode without `Auth.Enabled`, /api/health, etc. — are unaffected.
//
// rps: requests per second; burst: max burst size.
func RateLimitUserMiddleware(rps float64, burst int) gin.HandlerFunc {
	limiter := newUserRateLimiter(rate.Limit(rps), burst)
	return func(c *gin.Context) {
		userID := c.GetInt64("auth_user_id")
		if userID == 0 {
			c.Next()
			return
		}
		if !limiter.getLimiter(userID).Allow() {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": gin.H{"code": "RATE_LIMITED", "message": "too many requests, please try again later"},
			})
			return
		}
		c.Next()
	}
}
