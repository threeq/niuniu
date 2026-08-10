package agentproxy

import (
	"testing"
	"time"
)

func TestSessionHub_Subscribe_Broadcast(t *testing.T) {
	hub := NewSessionHub()
	defer hub.Stop()

	ch := hub.Subscribe(1, "window-a")

	// Broadcast a chunk
	hub.Broadcast(1, NewOutputEvent("chunk", "hello", "msg1", "assistant", 1))

	select {
	case ev := <-ch:
		if ev.Type != "chunk" || ev.Content != "hello" {
			t.Fatalf("unexpected event: %+v", ev)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for broadcast")
	}
}

func TestSessionHub_Unsubscribe(t *testing.T) {
	hub := NewSessionHub()
	defer hub.Stop()

	ch := hub.Subscribe(1, "window-a")
	hub.Unsubscribe(1, "window-a")

	// Send should not panic and not block
	hub.Broadcast(1, NewOutputEvent("chunk", "x", "msg1", "assistant", 1))

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("channel should be closed")
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("timeout")
	}
}

func TestSessionHub_MultipleSubscribers(t *testing.T) {
	hub := NewSessionHub()
	defer hub.Stop()

	ch1 := hub.Subscribe(1, "window-a")
	ch2 := hub.Subscribe(1, "window-b")

	hub.Broadcast(1, NewOutputEvent("chunk", "hello", "msg1", "assistant", 1))

	for i, ch := range []chan OutputEvent{ch1, ch2} {
		select {
		case ev := <-ch:
			if ev.Content != "hello" {
				t.Fatalf("ch%d: unexpected content %q", i+1, ev.Content)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("ch%d: timeout", i+1)
		}
	}
}

func TestSessionHub_BroadcastToWrongWorkspace(t *testing.T) {
	hub := NewSessionHub()
	defer hub.Stop()

	ch := hub.Subscribe(1, "window-a")

	// Broadcast to workspace 2, ch should not receive
	hub.Broadcast(2, NewOutputEvent("chunk", "wrong", "msg1", "assistant", 2))

	select {
	case <-ch:
		t.Fatal("should not receive broadcast for different workspace")
	case <-time.After(50 * time.Millisecond):
		// expected — no message
	}
}

func TestSessionHub_PingSentToAll(t *testing.T) {
	hub := NewSessionHub()
	defer hub.Stop()

	ch1 := hub.Subscribe(1, "window-a")
	ch2 := hub.Subscribe(1, "window-b")

	// Trigger cleanStale (which sends ping)
	hub.cleanStale()

	for i, ch := range []chan OutputEvent{ch1, ch2} {
		select {
		case ev := <-ch:
			if ev.Type != "ping" {
				t.Fatalf("ch%d: expected ping, got %q", i+1, ev.Type)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("ch%d: timeout waiting for ping", i+1)
		}
	}
}
