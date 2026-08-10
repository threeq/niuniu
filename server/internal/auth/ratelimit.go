package auth

import (
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

type ipEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type ipRateLimiter struct {
	mu      sync.Mutex
	entries map[string]*ipEntry
	rate    rate.Limit
	burst   int
	done    chan struct{}
}

func newIPRateLimiter(r rate.Limit, b int) *ipRateLimiter {
	rl := &ipRateLimiter{
		entries: make(map[string]*ipEntry),
		rate:    r,
		burst:   b,
		done:    make(chan struct{}),
	}
	go rl.cleanup(10*time.Minute, 1*time.Hour)
	return rl
}

func (rl *ipRateLimiter) getLimiter(ip string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	entry, exists := rl.entries[ip]
	if !exists {
		entry = &ipEntry{limiter: rate.NewLimiter(rl.rate, rl.burst)}
		rl.entries[ip] = entry
	}
	entry.lastSeen = time.Now()
	return entry.limiter
}

func (rl *ipRateLimiter) cleanup(interval, maxAge time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			rl.mu.Lock()
			cutoff := time.Now().Add(-maxAge)
			for ip, entry := range rl.entries {
				if entry.lastSeen.Before(cutoff) {
					delete(rl.entries, ip)
				}
			}
			rl.mu.Unlock()
		case <-rl.done:
			return
		}
	}
}

// RateLimitMiddleware returns a per-IP rate limiter.
// rps: requests per second; burst: max burst size.
func RateLimitMiddleware(rps float64, burst int) gin.HandlerFunc {
	limiter := newIPRateLimiter(rate.Limit(rps), burst)
	return func(c *gin.Context) {
		ip := clientIP(c)
		if !limiter.getLimiter(ip).Allow() {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": gin.H{"code": "RATE_LIMITED", "message": "too many requests, please try again later"},
			})
			return
		}
		c.Next()
	}
}

func clientIP(c *gin.Context) string {
	ip := c.ClientIP()
	if ip == "" {
		ip, _, _ = net.SplitHostPort(c.Request.RemoteAddr)
	}
	return ip
}
