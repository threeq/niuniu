package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestRateLimiter_AllowsUnderLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RateLimitMiddleware(5, 5))
	r.POST("/login", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })

	for i := range 5 {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/login", nil)
		req.RemoteAddr = "1.2.3.4:1234"
		r.ServeHTTP(w, req)
		assert.Equal(t, 200, w.Code, "request %d should succeed", i)
	}
}

func TestRateLimiter_BlocksOverLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RateLimitMiddleware(1, 1))
	r.POST("/login", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })

	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest("POST", "/login", nil)
	req1.RemoteAddr = "1.2.3.4:1234"
	r.ServeHTTP(w1, req1)
	assert.Equal(t, 200, w1.Code)

	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest("POST", "/login", nil)
	req2.RemoteAddr = "1.2.3.4:1234"
	r.ServeHTTP(w2, req2)
	assert.Equal(t, 429, w2.Code)
}

func TestRateLimiter_DifferentIPs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RateLimitMiddleware(1, 1))
	r.POST("/login", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })

	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest("POST", "/login", nil)
	req1.RemoteAddr = "1.1.1.1:1234"
	r.ServeHTTP(w1, req1)
	assert.Equal(t, 200, w1.Code)

	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest("POST", "/login", nil)
	req2.RemoteAddr = "2.2.2.2:1234"
	r.ServeHTTP(w2, req2)
	assert.Equal(t, 200, w2.Code)
}
