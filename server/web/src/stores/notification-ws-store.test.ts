import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useNotificationWSStore } from '@/stores/notification-ws-store';
import {
  logoutAndRedirect,
  refreshAccessToken,
} from '@/stores/auth-store';

// File-scoped auth-store mock: the recovery logic under test is exactly the
// calls into these functions, so the mock records them while keeping the
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

// Tracking WebSocket mock: close() synchronously fires onclose (the real one
// is async) — enough for the reconnect-gate logic under test.
class MockWebSocket {
  static instances: MockWebSocket[] = [];
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 3;
  readyState = MockWebSocket.CONNECTING;
  url: string;
  onopen: ((ev: Event) => unknown) | null = null;
  onmessage: ((ev: MessageEvent) => unknown) | null = null;
  onclose: ((ev: CloseEvent) => unknown) | null = null;
  onerror: ((ev: Event) => unknown) | null = null;
  constructor(url: string) {
    this.url = url;
    MockWebSocket.instances.push(this);
  }
  close() {
    if (this.readyState === MockWebSocket.CLOSED) return;
    this.readyState = MockWebSocket.CLOSED;
    this.onclose?.(new CloseEvent('close'));
  }
  send() {}
  emitOpen() {
    this.readyState = MockWebSocket.OPEN;
    this.onopen?.(new Event('open'));
  }
  emitClose() {
    this.close();
  }
}

// consecutiveErrors lives in the store state (not a module let) so a full
// state reset clears it between tests — a module-level counter would leak
// across cases and break the 3-strike threshold assertions.
function resetStore() {
  useNotificationWSStore.setState({
    ws: null,
    connected: false,
    lastMessage: null,
    reconnectCount: 0,
    permissionPendingByWorkspace: {},
    askUserPendingByWorkspace: {},
    consecutiveErrors: 0,
  });
}

describe('notification-ws-store token recovery', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    MockWebSocket.instances = [];
    vi.stubGlobal('WebSocket', MockWebSocket);
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

  it('refreshes and reconnects when the JWT is expired', async () => {
    mockedAuth.expired = true;
    mockedAuth.refreshResult = Promise.resolve('tok-2');
    mockedAuth.token = 'tok-2';

    useNotificationWSStore.getState().connect();
    expect(MockWebSocket.instances).toHaveLength(1);

    MockWebSocket.instances[0].emitClose();
    await vi.advanceTimersByTimeAsync(1000);

    expect(refreshAccessToken).toHaveBeenCalledTimes(1);
    expect(logoutAndRedirect).not.toHaveBeenCalled();
    expect(MockWebSocket.instances).toHaveLength(2);
    expect(MockWebSocket.instances[1].url).toContain('token=tok-2');
  });

  it('logs out and never schedules a reconnect when the refresh fails', async () => {
    mockedAuth.expired = true;
    mockedAuth.refreshResult = Promise.resolve(null);

    useNotificationWSStore.getState().connect();
    MockWebSocket.instances[0].emitClose();
    await vi.advanceTimersByTimeAsync(1000);

    expect(logoutAndRedirect).toHaveBeenCalledTimes(1);
    // The 30s backoff ceiling must not produce a new socket either.
    await vi.advanceTimersByTimeAsync(60_000);
    expect(MockWebSocket.instances).toHaveLength(1);
  });

  it('forces a refresh after 3 consecutive closes even with a valid JWT', async () => {
    mockedAuth.refreshResult = Promise.resolve('tok-2');
    mockedAuth.token = 'tok-2';

    useNotificationWSStore.getState().connect();
    // backoffMs is a module-level closure that doubles per reconnect and is
    // never reset between tests (only a 15s-after-open timer resets it), so
    // the effective delays here are 2s, 4s, 8s — advance generously.
    MockWebSocket.instances[0].emitClose();
    await vi.advanceTimersByTimeAsync(2000);
    MockWebSocket.instances[1].emitClose();
    await vi.advanceTimersByTimeAsync(4000);
    expect(refreshAccessToken).not.toHaveBeenCalled();

    MockWebSocket.instances[2].emitClose();
    await vi.advanceTimersByTimeAsync(8000);
    expect(refreshAccessToken).toHaveBeenCalledTimes(1);
  });

  // Personal edition runs with auth disabled and no token at all. A server
  // blip must keep plain-reconnecting: with no token there is nothing to
  // refresh, and firing the triple-fail branch would logout() (refreshToken
  // is null) and loop /login -> /workspaces -> reload forever.
  it('tokenless (personal edition): 3 consecutive closes reconnect without any refresh or logout', async () => {
    mockedAuth.token = null;
    mockedAuth.expired = false;
    mockedAuth.refreshResult = null;

    useNotificationWSStore.getState().connect();

    // backoffMs is a module-level closure that doubles per reconnect and is
    // never reset between tests (no open fires to schedule the reset), so the
    // effective delays depend on test order — capped by MAX_BACKOFF at 30s.
    // Advancing 60s per close always reaches the next reconnect.
    MockWebSocket.instances[0].emitClose();
    await vi.advanceTimersByTimeAsync(60_000);
    MockWebSocket.instances[1].emitClose();
    await vi.advanceTimersByTimeAsync(60_000);
    MockWebSocket.instances[2].emitClose();
    await vi.advanceTimersByTimeAsync(60_000);

    expect(refreshAccessToken).not.toHaveBeenCalled();
    expect(logoutAndRedirect).not.toHaveBeenCalled();
    // The triple-fail branch is skipped entirely — reconnect proceeds as usual.
    expect(MockWebSocket.instances).toHaveLength(4);
    expect(MockWebSocket.instances[3].url).not.toContain('token=');
  });
});
