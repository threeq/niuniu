import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useAgentSSEStore } from '@/stores/agent-sse-store';
import {
  logoutAndRedirect,
  refreshAccessToken,
} from '@/stores/auth-store';

// File-scoped auth-store mock: the recovery logic under test is exactly the
// calls into these four functions, so the mock records them while keeping the
// store's token plumbing (getAccessToken) scriptable per test.
vi.mock('@/stores/auth-store', () => ({
  getAccessToken: vi.fn(() => mockedAuth.token),
  isAccessTokenNearExpiry: vi.fn(() => mockedAuth.expired),
  refreshAccessToken: vi.fn(() => mockedAuth.refreshResult),
  logoutAndRedirect: vi.fn(),
}));

const mockedAuth = vi.mocked({
  token: null as string | null,
  expired: false,
  refreshResult: null as Promise<string | null> | null,
});

// Same tracking mock as agent-sse-store.test.ts: records every constructed
// instance so tests can count streams and drive lifecycle events.
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
  emitOpen() {
    this.readyState = MockEventSource.OPEN;
    this.onopen?.(new Event('open'));
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
    consecutiveErrors: 0,
  });
}

describe('agent-sse-store token recovery', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    MockEventSource.instances = [];
    vi.stubGlobal('EventSource', MockEventSource);
    mockedAuth.token = 'tok';
    mockedAuth.expired = false;
    mockedAuth.refreshResult = null;
    vi.mocked(refreshAccessToken).mockClear();
    vi.mocked(logoutAndRedirect).mockClear();
    resetStore();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it('refreshes and reconnects with the new token when the JWT is expired', async () => {
    mockedAuth.expired = true;
    mockedAuth.refreshResult = Promise.resolve('tok-2');
    mockedAuth.token = 'tok-2';

    useAgentSSEStore.getState().addWorkspace('254');
    await vi.advanceTimersByTimeAsync(300);
    expect(MockEventSource.instances).toHaveLength(1);

    MockEventSource.instances[0].emitError();
    await vi.advanceTimersByTimeAsync(2000);

    expect(refreshAccessToken).toHaveBeenCalledTimes(1);
    expect(logoutAndRedirect).not.toHaveBeenCalled();
    expect(MockEventSource.instances).toHaveLength(2);
    expect(MockEventSource.instances[1].url).toContain('token=tok-2');
  });

  it('logs out and stops reconnecting when the refresh fails', async () => {
    mockedAuth.expired = true;
    mockedAuth.refreshResult = Promise.resolve(null);

    useAgentSSEStore.getState().addWorkspace('254');
    await vi.advanceTimersByTimeAsync(300);
    MockEventSource.instances[0].emitError();
    await vi.advanceTimersByTimeAsync(2000);

    expect(logoutAndRedirect).toHaveBeenCalledTimes(1);
    expect(MockEventSource.instances).toHaveLength(1);
  });

  it('forces a refresh after 3 consecutive errors even with a valid JWT', async () => {
    mockedAuth.refreshResult = Promise.resolve('tok-2');
    mockedAuth.token = 'tok-2';

    useAgentSSEStore.getState().addWorkspace('254');
    await vi.advanceTimersByTimeAsync(300);

    // Errors 1 and 2: server hiccup territory — plain reconnect, no refresh.
    MockEventSource.instances[0].emitError();
    await vi.advanceTimersByTimeAsync(2000);
    MockEventSource.instances[1].emitError();
    await vi.advanceTimersByTimeAsync(2000);
    expect(refreshAccessToken).not.toHaveBeenCalled();

    // Error 3: same token keeps failing — try the refresh.
    MockEventSource.instances[2].emitError();
    await vi.advanceTimersByTimeAsync(2000);
    expect(refreshAccessToken).toHaveBeenCalledTimes(1);
    expect(MockEventSource.instances).toHaveLength(4);
  });

  it('clears the consecutive-error count on a successful open', async () => {
    mockedAuth.refreshResult = Promise.resolve('tok-2');
    mockedAuth.token = 'tok-2';

    useAgentSSEStore.getState().addWorkspace('254');
    await vi.advanceTimersByTimeAsync(300);

    // Two failures, then a successful open — the counter must reset.
    MockEventSource.instances[0].emitError();
    await vi.advanceTimersByTimeAsync(2000);
    MockEventSource.instances[1].emitOpen();
    MockEventSource.instances[1].emitError();
    await vi.advanceTimersByTimeAsync(2000);
    MockEventSource.instances[2].emitError();
    await vi.advanceTimersByTimeAsync(2000);
    expect(refreshAccessToken).not.toHaveBeenCalled();
  });
});
