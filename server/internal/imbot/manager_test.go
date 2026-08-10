package imbot

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

// fakeAdapter dispatches Connect behavior off cred.Config["mode"]:
//   "error" -> return immediately with an error (drives reconnect)
//   "block" -> block until ctx is cancelled (a healthy long connection)
type fakeAdapter struct{ typ ChannelType }

func (f *fakeAdapter) Type() ChannelType { return f.typ }

func (f *fakeAdapter) Connect(ctx context.Context, cred Credential, _ InboundHandler) error {
	switch fmt.Sprint(cred.Config["mode"]) {
	case "error":
		return fmt.Errorf("boom")
	default: // "block"
		<-ctx.Done()
		return nil
	}
}

func (f *fakeAdapter) Push(context.Context, Credential, OutboundMessage) error { return nil }
func (f *fakeAdapter) VerifyWebhook(*http.Request, Credential) (InboundEvent, error) {
	return InboundEvent{}, nil
}
func (f *fakeAdapter) Challenge(*http.Request) ([]byte, bool) { return nil, false }

type fakeProvider struct {
	mu       sync.Mutex
	channels map[int64]ManagedChannel
}

func newFakeProvider(chs ...ManagedChannel) *fakeProvider {
	p := &fakeProvider{channels: map[int64]ManagedChannel{}}
	for _, c := range chs {
		p.channels[c.ID] = c
	}
	return p
}

func (p *fakeProvider) set(mc ManagedChannel) {
	p.mu.Lock()
	p.channels[mc.ID] = mc
	p.mu.Unlock()
}
func (p *fakeProvider) del(id int64) {
	p.mu.Lock()
	delete(p.channels, id)
	p.mu.Unlock()
}

func (p *fakeProvider) ActiveStreamChannels(context.Context) ([]ManagedChannel, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]ManagedChannel, 0, len(p.channels))
	for _, c := range p.channels {
		out = append(out, c)
	}
	return out, nil
}

func (p *fakeProvider) ChannelByID(_ context.Context, id int64) (ManagedChannel, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	mc, ok := p.channels[id]
	return mc, ok, nil
}

func fastManager(provider ChannelProvider) *ConnectorManager {
	m := NewConnectorManager(provider, map[ChannelType]ChannelAdapter{
		ChannelLark:     &fakeAdapter{typ: ChannelLark},
		ChannelTelegram: &fakeAdapter{typ: ChannelTelegram},
	}, nil)
	m.backoffMin = time.Millisecond
	m.backoffMax = 5 * time.Millisecond
	return m
}

func eventually(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("condition not met within %s: %s", timeout, msg)
}

func mc(id, _ int64, typ ChannelType, mode string) ManagedChannel {
	return ManagedChannel{ID: id, Type: typ, Cred: Credential{Channel: typ, Config: map[string]any{"mode": mode}}}
}

// A failing channel must keep reconnecting (attempts climb well past 1).
func TestConnectorManager_ReconnectBackoff(t *testing.T) {
	p := newFakeProvider(mc(1, 100, ChannelTelegram, "error"))
	m := fastManager(p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()

	eventually(t, 2*time.Second, func() bool { return m.Attempts(1) >= 4 },
		"failing channel should retry repeatedly")
}

// One channel's failure/reconnect loop must not disturb another channel
// (including one belonging to a different project).
func TestConnectorManager_ProjectIsolation(t *testing.T) {
	p := newFakeProvider(
		mc(1, 100, ChannelTelegram, "error"), // project 100: always fails
		mc(2, 200, ChannelLark, "block"),      // project 200: healthy
	)
	m := fastManager(p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()

	// The failing channel keeps retrying...
	eventually(t, 2*time.Second, func() bool { return m.Attempts(1) >= 3 },
		"failing channel should retry")
	// ...while the healthy channel stays on its single connection.
	if got := m.Attempts(2); got != 1 {
		t.Fatalf("healthy channel reconnected unexpectedly: attempts=%d", got)
	}
	if m.ActiveCount() != 2 {
		t.Fatalf("expected 2 active connections, got %d", m.ActiveCount())
	}
}

// Create/enable and disable/delete must hot-reload without a restart.
func TestConnectorManager_HotReload(t *testing.T) {
	p := newFakeProvider()
	m := fastManager(p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()
	if m.ActiveCount() != 0 {
		t.Fatalf("expected 0 connections at start, got %d", m.ActiveCount())
	}

	// Create + enable.
	added := mc(9, 300, ChannelLark, "block")
	p.set(added)
	if err := m.ReloadChannel(ctx, 9); err != nil {
		t.Fatal(err)
	}
	eventually(t, time.Second, func() bool { return m.ActiveCount() == 1 }, "channel should connect")

	// Disable/delete: provider no longer returns it -> reload stops it.
	p.del(9)
	if err := m.ReloadChannel(ctx, 9); err != nil {
		t.Fatal(err)
	}
	eventually(t, time.Second, func() bool { return m.ActiveCount() == 0 }, "channel should stop")
}

// A fresh manager over the same provider re-establishes every active stream
// channel (process-restart recovery: connections are not persisted).
func TestConnectorManager_RestartRecovery(t *testing.T) {
	p := newFakeProvider(
		mc(1, 100, ChannelLark, "block"),
		mc(2, 200, ChannelLark, "block"),
	)
	m := fastManager(p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()
	eventually(t, time.Second, func() bool { return m.ActiveCount() == 2 },
		"both channels should be re-established from the store")
}
