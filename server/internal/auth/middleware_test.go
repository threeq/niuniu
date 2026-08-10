package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func setupTestRouter(enabled bool, secret string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Middleware(enabled, secret))
	r.GET("/test", func(c *gin.Context) {
		user, _ := c.Get("auth_user")
		if user != nil {
			claims := user.(*Claims)
			c.JSON(200, gin.H{"username": claims.Username})
		} else {
			c.JSON(200, gin.H{"username": "anonymous"})
		}
	})
	return r
}

func TestMiddleware_AuthDisabled(t *testing.T) {
	r := setupTestRouter(false, "secret")
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, 200, w.Code)
	assert.Contains(t, w.Body.String(), "anonymous")
}

func TestMiddleware_ValidToken(t *testing.T) {
	secret := "test-secret"
	token, _ := GenerateAccessToken(&Claims{UserID: 1, Username: "admin", Role: "admin"}, secret, 15*time.Minute)
	r := setupTestRouter(true, secret)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)
	assert.Equal(t, 200, w.Code)
	assert.Contains(t, w.Body.String(), "admin")
}

func TestMiddleware_NoToken(t *testing.T) {
	r := setupTestRouter(true, "secret")
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, 401, w.Code)
}

func TestMiddleware_InvalidToken(t *testing.T) {
	r := setupTestRouter(true, "secret")
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	r.ServeHTTP(w, req)
	assert.Equal(t, 401, w.Code)
}

func TestMiddleware_QueryParamToken(t *testing.T) {
	secret := "test-secret"
	token, _ := GenerateAccessToken(&Claims{UserID: 1, Username: "admin", Role: "admin"}, secret, 15*time.Minute)
	r := setupTestRouter(true, secret)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test?token="+token, nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, 200, w.Code)
	assert.Contains(t, w.Body.String(), "admin")
}
