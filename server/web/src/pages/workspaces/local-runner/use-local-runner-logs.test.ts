import { renderHook, act } from '@testing-library/react';
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { useLocalRunnerLogs } from './use-local-runner-logs';
import { useLocalRunnerStore } from '@/stores/local-runner-store';

// ---------------------------------------------------------------------------
// Mock the access token — the hook skips connect without one.
// ---------------------------------------------------------------------------
let token: string | null = 'jwt-token';
vi.mock('@/stores/auth-store', () => ({
  getAccessToken: () => token,
}));

// ---------------------------------------------------------------------------
// Controllable fake WebSocket (jsdom has no live WS).
// ---------------------------------------------------------------------------
const sockets: FakeWebSocket[] = [];

class FakeWebSocket {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;

  static instances: FakeWebSocket[] = sockets;

  readonly url: string;
  readyState = FakeWebSocket.CONNECTING;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  closeCalls = 0;

  constructor(url: string) {
    this.url = url;
    sockets.push(this);
  }

  close() {
    this.closeCalls += 1;
    this.readyState = FakeWebSocket.CLOSED;
    this.onclose?.();
  }

  // Test helpers ------------------------------------------------------------
  emitOpen() {
    this.readyState = FakeWebSocket.OPEN;
    this.onopen?.();
  }

  emitMessage(data: unknown) {
    this.onmessage?.({ data: JSON.stringify(data) });
  }

  emitClose() {
    this.readyState = FakeWebSocket.CLOSED;
    this.onclose?.();
  }
}

const WS = 'ws-99';

beforeEach(() => {
  vi.useFakeTimers();
  token = 'jwt-token';
  sockets.length = 0;
  useLocalRunnerStore.setState({ byWorkspace: {} });
  vi.stubGlobal('WebSocket', FakeWebSocket as unknown as typeof WebSocket);
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

function lastSocket(): FakeWebSocket {
  return sockets[sockets.length - 1];
}

describe('useLocalRunnerLogs', () => {
  it('does not connect when disabled', () => {
    renderHook(() => useLocalRunnerLogs(WS, false));
    expect(sockets).toHaveLength(0);
  });

  it('skips connect when there is no token', () => {
    token = null;
    renderHook(() => useLocalRunnerLogs(WS, true));
    expect(sockets).toHaveLength(0);
  });

  it('opens a socket with the token query param when enabled', () => {
    renderHook(() => useLocalRunnerLogs(WS, true));
    expect(sockets).toHaveLength(1);
    expect(lastSocket().url).toContain(`/ws/workspaces/${WS}/local-runner/logs`);
    expect(lastSocket().url).toContain('token=jwt-token');
  });

  it('appends messages with a wall-clock ts and the server level/text', () => {
    const now = 1_700_000_000_000;
    vi.setSystemTime(now);

    renderHook(() => useLocalRunnerLogs(WS, true));
    act(() => {
      lastSocket().emitOpen();
      // Server ts is a monotonic ordinal (1) — the client must ignore it.
      lastSocket().emitMessage({ id: 'a', ts: 1, level: 'stdout', text: 'hello' });
      lastSocket().emitMessage({ id: 'b', ts: 2, level: 'command', text: 'npm run build' });
    });

    const logs = useLocalRunnerStore.getState().getState(WS).logs;
    expect(logs).toHaveLength(2);
    expect(logs[0]).toMatchObject({ ts: now, level: 'stdout', text: 'hello' });
    expect(logs[1]).toMatchObject({ ts: now, level: 'command', text: 'npm run build' });
  });

  it('ignores malformed / unknown-level messages', () => {
    renderHook(() => useLocalRunnerLogs(WS, true));
    act(() => {
      lastSocket().emitOpen();
      lastSocket().emitMessage({ id: 'x', level: 'bogus', text: 'nope' });
      lastSocket().emitMessage({ id: 'y', level: 'stdout' }); // missing text
    });
    expect(useLocalRunnerStore.getState().getState(WS).logs).toHaveLength(0);
  });

  it('drives status to error on an unexpected close and schedules a reconnect', () => {
    renderHook(() => useLocalRunnerLogs(WS, true));
    act(() => {
      lastSocket().emitClose();
    });

    const state = useLocalRunnerStore.getState().getState(WS);
    expect(state.status).toBe('error');
    expect(state.error).toBeTruthy();

    // Backoff reconnect fires and opens a fresh socket.
    act(() => {
      vi.advanceTimersByTime(1000);
    });
    expect(sockets).toHaveLength(2);
  });

  it('clears a prior error on reopen', () => {
    renderHook(() => useLocalRunnerLogs(WS, true));
    act(() => {
      lastSocket().emitClose();
    });
    expect(useLocalRunnerStore.getState().getState(WS).status).toBe('error');

    act(() => {
      vi.advanceTimersByTime(1000);
      lastSocket().emitOpen();
    });
    expect(useLocalRunnerStore.getState().getState(WS).status).toBe('active');
  });

  it('closes the socket and does not reconnect after intentional cleanup', () => {
    const { unmount } = renderHook(() => useLocalRunnerLogs(WS, true));
    const socket = lastSocket();
    act(() => socket.emitOpen());

    unmount();
    expect(socket.closeCalls).toBeGreaterThanOrEqual(1);
    expect(useLocalRunnerStore.getState().getState(WS).status).not.toBe('error');

    // No reconnect after intentional close — advancing timers opens nothing new.
    act(() => {
      vi.advanceTimersByTime(60_000);
    });
    expect(sockets).toHaveLength(1);
  });
});
