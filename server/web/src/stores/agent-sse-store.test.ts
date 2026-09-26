import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useAgentSSEStore } from '@/stores/agent-sse-store';

// Tracking EventSource mock: records every constructed instance so tests can
// assert how many streams the store opened and drive lifecycle events.
class MockEventSource {
  static instances: MockEventSource[] = [];
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  readyState = MockEventSource.CONNECTING;
  url: string;
  onopen: ((ev: Event) => unknown) | null = null;
  onmessage: ((ev: MessageEvent) => unknown) | null = null;
  onerror: ((ev: Event) => unknown) | null = null;
  constructor(url: string) {
    this.url = url;
    MockEventSource.instances.push(this);
  }
  close() {
    this.readyState = MockEventSource.CLOSED;
  }
  emitError() {
    this.onerror?.(new Event('error'));
  }
}

function resetStore() {
  useAgentSSEStore.setState({
    es: null,
    subscribedWorkspaces: new Set(),
    handlers: new Map(),
    globalHandlers: new Set(),
    lastEventTs: 0,
    reconnectTimer: null,
    intentionalClose: false,
    onReconnectCallbacks: new Set(),
  });
}

describe('agent-sse-store reconnection', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    MockEventSource.instances = [];
    vi.stubGlobal('EventSource', MockEventSource);
    resetStore();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  // Regression: navigating away from the last workspace empties the
  // subscription set and disconnect()s, latching intentionalClose=true. Every
  // later scheduleReconnect/onerror is gated on that flag, and only connect()
  // clears it — so the store stayed dead (no SSE events at all) until a full
  // page reload re-ran an addHandler mount. The server log showed the matching
  // signature: a connection dying right after a workspace switch, then no
  // reconnect attempt until the next fresh page load.
  it('reconnects when a workspace is added after the subscription set was emptied', () => {
    const store = useAgentSSEStore.getState();

    store.addWorkspace('932');
    vi.advanceTimersByTime(300);
    expect(MockEventSource.instances).toHaveLength(1);

    store.removeWorkspace('932');
    // Stream is down (previous behavior also latched intentionalClose here,
    // which permanently blocked every later reconnect until a page reload).
    expect(useAgentSSEStore.getState().es).toBeNull();

    store.addWorkspace('933');
    vi.advanceTimersByTime(300);
    expect(MockEventSource.instances.length).toBeGreaterThanOrEqual(2);
    expect(MockEventSource.instances.at(-1)!.url).toContain('workspaces=933');
  });

  it('reconnects when a workspace re-subscribes while the stream is closed', () => {
    const store = useAgentSSEStore.getState();

    store.addWorkspace('932');
    vi.advanceTimersByTime(300);
    const es1 = MockEventSource.instances[0];
    es1.close();

    // Same workspace again (e.g. returning to the page): the store must notice
    // the dead stream and reopen it, not silently keep the closed one.
    store.addWorkspace('932');
    vi.advanceTimersByTime(300);
    expect(MockEventSource.instances.length).toBe(2);
  });

  it('reconnects after a stream error via the 2s backoff', () => {
    const store = useAgentSSEStore.getState();

    store.addWorkspace('932');
    vi.advanceTimersByTime(300);
    MockEventSource.instances[0].emitError();
    vi.advanceTimersByTime(2000);
    expect(MockEventSource.instances.length).toBe(2);
  });
});
