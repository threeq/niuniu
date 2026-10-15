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
    // Backoff: 1s, 2s, 4s — advance generously between closes.
    MockWebSocket.instances[0].emitClose();
    await vi.advanceTimersByTimeAsync(2000);
    MockWebSocket.instances[1].emitClose();
    await vi.advanceTimersByTimeAsync(4000);
    expect(refreshAccessToken).not.toHaveBeenCalled();

    MockWebSocket.instances[2].emitClose();
    await vi.advanceTimersByTimeAsync(8000);
    expect(refreshAccessToken).toHaveBeenCalledTimes(1);
  });
});
