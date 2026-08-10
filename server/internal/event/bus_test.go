package event_test

import (
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBus_Subscribe_Receive(t *testing.T) {
	bus := event.NewBus()
	defer bus.Close()
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)
	e := event.OutputEvent{Type: event.EventAgentDone, Content: "finished", WorkspaceId: 1}
	bus.Publish(e)
	select {
	case got := <-ch:
		assert.Equal(t, event.EventAgentDone, got.Type)
		assert.Equal(t, "finished", got.Content)
	case <-time.After(time.Second):
		require.Fail(t, "timed out waiting for event")
	}
}

func TestBus_MultipleSubscribers(t *testing.T) {
	bus := event.NewBus()
	defer bus.Close()
	ch1 := bus.Subscribe()
	ch2 := bus.Subscribe()
	defer bus.Unsubscribe(ch1)
	defer bus.Unsubscribe(ch2)
	e := event.OutputEvent{Type: event.EventAgentDone, Content: "test"}
	bus.Publish(e)
	select {
	case got := <-ch1:
		assert.Equal(t, event.EventAgentDone, got.Type)
	case <-time.After(time.Second):
		require.Fail(t, "ch1 timed out")
	}
	select {
	case got := <-ch2:
		assert.Equal(t, event.EventAgentDone, got.Type)
	case <-time.After(time.Second):
		require.Fail(t, "ch2 timed out")
	}
}

func TestBus_Unsubscribe(t *testing.T) {
	bus := event.NewBus()
	defer bus.Close()
	ch := bus.Subscribe()
	bus.Unsubscribe(ch)
	// After unsubscribe, the channel should not receive events
	e := event.OutputEvent{Type: event.EventAgentDone}
	bus.Publish(e)
	// Give it a small window to ensure no event arrives
	select {
	case <-ch:
		// Receiving means unsubscribe didn't work
		require.Fail(t, "should not receive after unsubscribe")
	case <-time.After(50 * time.Millisecond):
		// Expected: timeout means no event received
	}
}
