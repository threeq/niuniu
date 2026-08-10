package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestListenServeEphemeralPort verifies that Listen+Serve expose the actual
// bound TCP address when asking for :0, and that the server is reachable.
// Constructs Server directly (bypassing New) so we don't depend on DB/config
// fixtures — this is a narrow test of the listener-split behavior.
func TestListenServeEphemeralPort(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/api/health", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	s := &Server{engine: engine}

	ln, err := s.Listen("127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	addr := ln.Addr().String()
	require.NotContains(t, addr, ":0", "expected a concrete port, got %s", addr)

	go func() { _ = s.Serve(ln) }()

	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/api/health")
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("server never became reachable: %v", lastErr)
}
