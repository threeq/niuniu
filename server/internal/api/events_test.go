package api_test

import (
	"bufio"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/api"
	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventsHandler_Stream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	bus := event.NewBus()
	defer bus.Close()
	h := api.NewEventsHandler(bus)

	r := gin.New()
	r.GET("/api/events/stream", h.Stream)

	srv := httptest.NewServer(r)
	defer srv.Close()

	// Use a channel to communicate between goroutines
	done := make(chan bool)
	var receivedLine string

	// Start goroutine to make the request and read from stream
	go func() {
		req, _ := http.NewRequest("GET", srv.URL+"/api/events/stream", nil)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				receivedLine = line
				break
			}
		}
		done <- true
	}()

	// Give the connection a moment to establish
	time.Sleep(100 * time.Millisecond)

	// Publish an event
	bus.Publish(event.OutputEvent{
		Type:        event.EventAgentDone,
		Content:     "task complete",
		WorkspaceId: 42,
	})

	// Wait for the goroutine to read the event (with timeout)
	select {
	case <-done:
		require.NotEmpty(t, receivedLine, "should have received an SSE data line")
		assert.Contains(t, receivedLine, "agent_done")
		assert.Contains(t, receivedLine, "task complete")
	case <-time.After(3 * time.Second):
		require.Fail(t, "timed out waiting for event")
	}
}

// TestEventsHandler_LastEventID verifies that a reconnecting client supplying
// Last-Event-ID gets the missed events replayed before the live stream.
func TestEventsHandler_LastEventID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	bus := event.NewBus()
	defer bus.Close()
	ring := event.NewRing(1000)
	h := api.NewEventsHandlerWithRing(bus, ring)

	r := gin.New()
	r.GET("/api/events/stream", h.Stream)

	srv := httptest.NewServer(r)
	defer srv.Close()

	// Pre-populate the ring with 3 events (simulating a previous connection).
	ev1 := event.OutputEvent{Type: event.EventText, Content: "event-one", WorkspaceId: 1}
	ev2 := event.OutputEvent{Type: event.EventText, Content: "event-two", WorkspaceId: 1}
	ev3 := event.OutputEvent{Type: event.EventText, Content: "event-three", WorkspaceId: 1}
	id1 := ring.Append(ev1)
	ring.Append(ev2)
	ring.Append(ev3)

	// Reconnect with Last-Event-ID = id1 (first event), expecting events 2 and 3.
	done := make(chan []string, 1)
	go func() {
		req, _ := http.NewRequest("GET", srv.URL+"/api/events/stream", nil)
		req.Header.Set("Last-Event-ID", fmt.Sprintf("%d", id1))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- nil
			return
		}
		defer resp.Body.Close()

		var lines []string
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				lines = append(lines, line)
				if len(lines) >= 2 {
					break
				}
			}
		}
		done <- lines
	}()

	select {
	case lines := <-done:
		require.Len(t, lines, 2, "expected exactly 2 replayed events (ev2 and ev3)")
		assert.Contains(t, lines[0], "event-two", "first replayed line should be ev2")
		assert.Contains(t, lines[1], "event-three", "second replayed line should be ev3")
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Last-Event-ID replay")
	}
}
