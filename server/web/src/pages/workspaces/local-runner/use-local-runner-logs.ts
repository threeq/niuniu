import { useEffect } from 'react';
import i18n from '@/i18n';
import { getAccessToken } from '@/stores/auth-store';
import { localRunnerApi } from '@/lib/local-runner-api';
import {
  useLocalRunnerStore,
  type LocalRunnerLogEntry,
} from '@/stores/local-runner-store';

const MAX_BACKOFF = 30_000;

type LogLevel = LocalRunnerLogEntry['level'];

const LOG_LEVELS: readonly LogLevel[] = [
  'command',
  'stdout',
  'stderr',
  'system',
];

/** Wire shape of one log entry — mirrors the server contract (#526·子D). */
interface WireLog {
  id: string;
  ts: number;
  level: LogLevel;
  text: string;
}

/**
 * Runtime shape guard for a parsed WS message. The server `ts` is a monotonic
 * ordinal we intentionally ignore — the client stamps its own wall-clock time
 * on receipt (see `appendLog` below).
 */
function isWireLog(value: unknown): value is WireLog {
  if (typeof value !== 'object' || value === null) return false;
  const v = value as Record<string, unknown>;
  return (
    typeof v.text === 'string' &&
    typeof v.level === 'string' &&
    (LOG_LEVELS as readonly string[]).includes(v.level)
  );
}

/**
 * Subscribe to the per-workspace local-runner log stream (#526·子D).
 *
 * When `enabled` (runner bound, i.e. status !== 'unbound') this opens a
 * WebSocket to `logsStreamUrl(workspaceId)` — the server replays up to the last
 * 500 entries on connect, then streams live. Each message is validated, given a
 * wall-clock `ts` (`Date.now()` — the server `ts` is a monotonic ordinal unfit
 * for `formatTime`), and pushed via the store's `appendLog`.
 *
 * The stream is READ-ONLY and not gated: an offline runner just means no new
 * lines. On an unexpected error/close while enabled we drive the store's
 * `error` state and reconnect with bounded exponential backoff. On unmount /
 * disable / workspace change the socket is closed, timers cleared, and
 * `intentionalClose` set so the close handler does NOT flip to error — no leaks.
 *
 * URL/protocol/token construction mirrors `notification-ws-store`.
 */
export function useLocalRunnerLogs(workspaceId: string, enabled: boolean): void {
  useEffect(() => {
    if (!enabled) return;

    const token = getAccessToken();
    // No token → skip connect entirely (the stream requires auth).
    if (!token) return;

    let ws: WebSocket | null = null;
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
    let backoffMs = 1000;
    let intentionalClose = false;

    function clearTimers() {
      if (reconnectTimer) {
        clearTimeout(reconnectTimer);
        reconnectTimer = null;
      }
    }

    function scheduleReconnect() {
      if (intentionalClose) return;
      reconnectTimer = setTimeout(() => {
        connect();
      }, backoffMs);
      backoffMs = Math.min(backoffMs * 2, MAX_BACKOFF);
    }

    function connect() {
      const authToken = getAccessToken();
      if (!authToken) return;

      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const path = localRunnerApi.logsStreamUrl(workspaceId);
      const url = `${protocol}//${window.location.host}${path}?token=${encodeURIComponent(authToken)}`;
      const socket = new WebSocket(url);
      ws = socket;

      socket.onopen = () => {
        // Connection healthy → clear any prior stream-level error. Server owns
        // the authoritative status, so only reset an error we set ourselves.
        const state = useLocalRunnerStore.getState().getState(workspaceId);
        if (state.status === 'error') {
          useLocalRunnerStore.getState().setStatus(workspaceId, 'active', null);
        }
      };

      socket.onmessage = (event) => {
        try {
          const parsed: unknown = JSON.parse(event.data as string);
          if (!isWireLog(parsed)) return;
          useLocalRunnerStore.getState().appendLog(workspaceId, {
            ts: Date.now(),
            level: parsed.level,
            text: parsed.text,
          });
        } catch (e) {
          console.error('[LocalRunnerLogs] parse error', e);
        }
      };

      socket.onerror = () => socket.close();

      socket.onclose = () => {
        if (ws === socket) ws = null;
        if (intentionalClose) return;
        useLocalRunnerStore
          .getState()
          .setStatus(
            workspaceId,
            'error',
            i18n.t('workspaces:localRunner.bar.streamError'),
          );
        scheduleReconnect();
      };
    }

    connect();

    return () => {
      intentionalClose = true;
      clearTimers();
      if (ws) {
        ws.close();
        ws = null;
      }
    };
  }, [workspaceId, enabled]);
}
