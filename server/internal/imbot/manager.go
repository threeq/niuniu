package imbot

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// ManagedChannel is the minimal, already-decrypted view of an active channel
// the ConnectorManager needs to dial it. Channels are owner-level with no home
// project; routing is resolved per-chat (im_bot_chats.project_id) once an event
// arrives, so no project is carried here.
type ManagedChannel struct {
	ID   int64
	Type ChannelType
	Cred Credential
}

// ChannelProvider supplies the manager with the set of channels to keep
// connected. The service layer implements it (decrypting credentials from the
// store); the manager stays free of any DB/crypto coupling, which is what
// makes it unit-testable in isolation.
type ChannelProvider interface {
	// ActiveStreamChannels returns every status=active, connection_mode=stream
	// channel across all projects (used at process start / restart recovery).
	ActiveStreamChannels(ctx context.Context) ([]ManagedChannel, error)
	// ChannelByID returns one channel if it is currently an active stream
	// channel; ok=false means it should not be connected (disabled/deleted/
	// webhook-mode).
	ChannelByID(ctx context.Context, id int64) (mc ManagedChannel, ok bool, err error)
}

// ConnectorManager keeps one outbound long-connection goroutine per active
// stream channel, reconnecting with exponential backoff, hot-reloading on
// channel create/enable/disable/delete, and re-establishing every connection
// from the store on process restart (connections are process-held, never
// persisted; credentials live encrypted in the store).
type ConnectorManager struct {
	provider ChannelProvider
	adapters map[ChannelType]ChannelAdapter
	handler  InboundHandler

	mu      sync.Mutex
	conns   map[int64]*channelConn
	baseCtx context.Context
	started bool

	// Tunables (overridable in tests for fast reconnect assertions).
	backoffMin time.Duration
	backoffMax time.Duration

	// onConnectAttempt, when set, is invoked before each adapter.Connect call
	// (attempt is 1-based). Test-only hook; nil in production.
	onConnectAttempt func(channelID int64, attempt int)
}

type channelConn struct {
	cancel   context.CancelFunc
	done     chan struct{}
	attempts atomic.Int64
}

// NewConnectorManager builds a manager. adapters maps each supported
// ChannelType to its (stateless) adapter; handler receives every inbound event
// (unused by W1 outbound-only flows, but wired so W2 inbound drops in).
func NewConnectorManager(provider ChannelProvider, adapters map[ChannelType]ChannelAdapter, handler InboundHandler) *ConnectorManager {
	if handler == nil {
		handler = func(context.Context, InboundEvent) {}
	}
	return &ConnectorManager{
		provider:   provider,
		adapters:   adapters,
		handler:    handler,
		conns:      make(map[int64]*channelConn),
		backoffMin: 1 * time.Second,
		backoffMax: 60 * time.Second,
	}
}

// Start records the base context and connects every currently active stream
// channel. Safe to call once at server boot; the base ctx should live for the
// process (cancel it, or call Stop, to tear everything down).
func (m *ConnectorManager) Start(ctx context.Context) error {
	m.mu.Lock()
	m.baseCtx = ctx
	m.started = true
	m.mu.Unlock()

	channels, err := m.provider.ActiveStreamChannels(ctx)
	if err != nil {
		return err
	}
	for _, mc := range channels {
		m.StartChannel(mc)
	}
	slog.Info("imbot: connector manager started", "channels", len(channels))
	return nil
}

// StartChannel launches (or restarts) the connection goroutine for one
// channel. Idempotent: an existing connection for the same id is stopped first.
func (m *ConnectorManager) StartChannel(mc ManagedChannel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.startChannelLocked(mc)
}

func (m *ConnectorManager) startChannelLocked(mc ManagedChannel) {
	if !m.started || m.baseCtx == nil {
		// Not started yet (e.g. a channel created during boot before Start);
		// Start() will pick it up from the store.
		return
	}
	if _, ok := m.adapters[mc.Type]; !ok {
		slog.Warn("imbot: no adapter registered for channel type", "type", mc.Type, "channel", mc.ID)
		return
	}
	if existing, ok := m.conns[mc.ID]; ok {
		existing.cancel()
		<-existing.done
		delete(m.conns, mc.ID)
	}
	ctx, cancel := context.WithCancel(m.baseCtx)
	cc := &channelConn{cancel: cancel, done: make(chan struct{})}
	m.conns[mc.ID] = cc
	go m.runChannel(ctx, mc, cc)
}

// StopChannel cancels and removes the connection for one channel (disable/
// delete). No-op if not connected.
func (m *ConnectorManager) StopChannel(id int64) {
	m.mu.Lock()
	cc, ok := m.conns[id]
	if ok {
		delete(m.conns, id)
	}
	m.mu.Unlock()
	if ok {
		cc.cancel()
		<-cc.done
	}
}

// ReloadChannel re-evaluates one channel against the store: if it is an active
// stream channel it is (re)started with fresh credentials, otherwise it is
// stopped. Call after any create/update/delete so the running connections
// track the DB without a restart.
func (m *ConnectorManager) ReloadChannel(ctx context.Context, id int64) error {
	mc, ok, err := m.provider.ChannelByID(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		m.StopChannel(id)
		return nil
	}
	m.StartChannel(mc)
	return nil
}

// Stop tears down every connection and blocks until all goroutines exit.
func (m *ConnectorManager) Stop() {
	m.mu.Lock()
	conns := m.conns
	m.conns = make(map[int64]*channelConn)
	m.started = false
	m.mu.Unlock()
	for _, cc := range conns {
		cc.cancel()
	}
	for _, cc := range conns {
		<-cc.done
	}
}

// ActiveCount reports how many channel goroutines are currently registered.
func (m *ConnectorManager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.conns)
}

// Attempts reports the current connect-attempt counter for a channel (0 if not
// tracked). Primarily for tests / status.
func (m *ConnectorManager) Attempts(id int64) int64 {
	m.mu.Lock()
	cc, ok := m.conns[id]
	m.mu.Unlock()
	if !ok {
		return 0
	}
	return cc.attempts.Load()
}

func (m *ConnectorManager) runChannel(ctx context.Context, mc ManagedChannel, cc *channelConn) {
	defer close(cc.done)
	adapter := m.adapters[mc.Type]
	handler := m.wrapHandler(mc)
	var attempt int
	for {
		if ctx.Err() != nil {
			return
		}
		attempt++
		cc.attempts.Store(int64(attempt))
		if m.onConnectAttempt != nil {
			m.onConnectAttempt(mc.ID, attempt)
		}
		err := adapter.Connect(ctx, mc.Cred, handler)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Warn("imbot: channel connection ended, will reconnect",
				"channel", mc.ID, "type", mc.Type, "attempt", attempt, "error", err)
		} else {
			// Graceful return: reset the backoff ladder for the next dial.
			attempt = 0
		}
		if !m.sleep(ctx, m.backoffFor(attempt)) {
			return
		}
	}
}

// wrapHandler stamps the channel id onto every event before the service sees
// it (adapters cannot know their DB row id).
func (m *ConnectorManager) wrapHandler(mc ManagedChannel) InboundHandler {
	return func(ctx context.Context, ev InboundEvent) {
		ev.ChannelID = mc.ID
		ev.Channel = mc.Type
		m.handler(ctx, ev)
	}
}

// backoffFor returns min*2^(attempt-1) capped at max; attempt<=1 yields min.
func (m *ConnectorManager) backoffFor(attempt int) time.Duration {
	if attempt <= 1 {
		return m.backoffMin
	}
	d := m.backoffMin
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= m.backoffMax {
			return m.backoffMax
		}
	}
	return d
}

// sleep waits for d or ctx cancellation; returns false if ctx was cancelled.
func (m *ConnectorManager) sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
