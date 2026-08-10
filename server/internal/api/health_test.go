package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHealthHandler_Health(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := api.NewHealthHandler("test-server-id", false)

	// Allow a small amount of time to pass so uptime_seconds is > 0
	time.Sleep(1 * time.Millisecond)

	r := gin.New()
	r.GET("/api/health", h.Health)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/health", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, "ok", resp["status"])
	assert.NotEmpty(t, resp["version"])

	uptime, ok := resp["uptime_seconds"].(float64)
	require.True(t, ok, "uptime_seconds should be a number")
	assert.GreaterOrEqual(t, uptime, float64(0))

	assert.Equal(t, "test-server-id", resp["server_id"])
	assert.Equal(t, false, resp["auth_enabled"])
	assert.Equal(t, float64(1), resp["api_version"])
}
