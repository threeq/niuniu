package connection_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu-desktop/internal/connection"
	"github.com/stretchr/testify/assert"
)

func TestSSEListener_ReceivesEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, _ := w.(http.Flusher)
		fmt.Fprintf(w, "event: agent_done\ndata: {\"type\":\"agent_done\",\"content\":\"ok\",\"workspaceId\":1}\n\n")
		flusher.Flush()
		time.Sleep(500 * time.Millisecond)
	}))
	defer srv.Close()

	var received atomic.Bool
	listener := connection.NewSSEListener(srv.URL+"/api/events/stream", func(eventType, content string, workspaceId int64) {
		if eventType == "agent_done" {
			received.Store(true)
		}
	})
	listener.Start()
	defer listener.Stop()

	time.Sleep(300 * time.Millisecond)
	assert.True(t, received.Load(), "should have received agent_done event")
}
