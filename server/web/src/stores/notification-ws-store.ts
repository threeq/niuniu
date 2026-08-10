import { create } from 'zustand';
import { getAccessToken } from '@/stores/auth-store';
import { useOrgStore } from '@/stores/org-store';
import type { Notification } from '@/lib/notification-dispatcher';

const HEARTBEAT_TIMEOUT = 35_000;
const MAX_BACKOFF = 30_000;

interface NotificationWSState {
  ws: WebSocket | null;
  connected: boolean;
  lastMessage: Notification | null;
  reconnectCount: number;
  /**
   * Per-workspace set of pending permission request IDs.
   * Populated from `topic=permission_pending` notify events; consumed by
   * cross-page surfaces (global nav badge, workspace list) so users on
   * /projects, /repositories, /settings see when an agent is waiting for
   * authorization in another workspace.
   *
   * Storing IDs (not just a count) makes the redelivery case idempotent —
   * if the backend re-broadcasts the same `request` event, Set semantics
   * dedupe.
   */
  permissionPendingByWorkspace: Record<number, Set<number>>;
  /**
   * Per-workspace set of pending ask-user request IDs. Symmetric with
   * permissionPendingByWorkspace — populated from `topic=ask_user_pending`
   * notify events so cross-page surfaces can show "an agent is waiting for
   * an answer in workspace X" without subscribing to that workspace's SSE.
   */
  askUserPendingByWorkspace: Record<number, Set<number>>;
  connect: () => void;
  disconnect: () => void;
}

export const useNotificationWSStore = create<NotificationWSState>((set, get) => {
  let heartbeatTimer: ReturnType<typeof setTimeout> | null = null;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  let backoffMs = 1000;
  let backoffResetTimer: ReturnType<typeof setTimeout> | null = null;
  let intentionalClose = false;

  function clearTimers() {
    if (heartbeatTimer) { clearTimeout(heartbeatTimer); heartbeatTimer = null; }
    if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null; }
    if (backoffResetTimer) { clearTimeout(backoffResetTimer); backoffResetTimer = null; }
  }

  function resetHeartbeat() {
    if (heartbeatTimer) clearTimeout(heartbeatTimer);
    heartbeatTimer = setTimeout(() => {
      const ws = get().ws;
      if (ws) ws.close();
    }, HEARTBEAT_TIMEOUT);
  }

  function scheduleReconnect() {
    if (intentionalClose) return;
    reconnectTimer = setTimeout(() => {
      get().connect();
    }, backoffMs);
    backoffMs = Math.min(backoffMs * 2, MAX_BACKOFF);
  }

  return {
    ws: null,
    connected: false,
    lastMessage: null,
    reconnectCount: 0,
    permissionPendingByWorkspace: {},
    askUserPendingByWorkspace: {},

    connect() {
      const state = get();
      if (state.ws) state.ws.close();
      clearTimers();
      intentionalClose = false;

      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const token = getAccessToken();
      const tokenParam = token ? `?token=${encodeURIComponent(token)}` : '';
      const url = `${protocol}//${window.location.host}/ws/notify${tokenParam}`;
      const ws = new WebSocket(url);

      ws.onopen = () => {
        set({ connected: true });
        resetHeartbeat();
        backoffResetTimer = setTimeout(() => { backoffMs = 1000; }, 15_000);
      };

      ws.onmessage = (event) => {
        try {
          const msg: Notification = JSON.parse(event.data);
          resetHeartbeat();
          if (msg.topic === 'ping') {
            if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: 'pong' }));
            return;
          }
          if (msg.topic === 'org_member') {
            // Org membership changed — refresh the org store so OwnerBadge,
            // OwnerPicker, and resource lists reflect the updated membership.
            const orgStore = useOrgStore.getState();
            orgStore.invalidate();
            orgStore.fetch();
          }
          if (msg.topic === 'permission_pending' || msg.topic === 'ask_user_pending') {
            // Backend wire shape mirrors service/permission.go + service/ask_user.go:
            //   { topic, action: 'request' | 'resolved', id: <requestID>,
            //     extra: { workspaceId: <number>, ... } }
            // OwnerType/OwnerID are stripped by hub-side filtering and never
            // reach the wire.
            const extra = (msg.extra as Record<string, unknown> | undefined) ?? {};
            const wsRaw = extra.workspaceId;
            const workspaceId = typeof wsRaw === 'number' ? wsRaw : Number(wsRaw);
            const requestId = msg.id;
            if (Number.isFinite(workspaceId) && workspaceId > 0 && typeof requestId === 'number') {
              set((state) => {
                const isPermission = msg.topic === 'permission_pending';
                const byWs = isPermission
                  ? state.permissionPendingByWorkspace
                  : state.askUserPendingByWorkspace;
                const prev = byWs[workspaceId];
                const next = new Set(prev ?? []);
                if (msg.action === 'request') {
                  next.add(requestId);
                } else if (msg.action === 'resolved') {
                  next.delete(requestId);
                }
                const updated = { ...byWs };
                if (next.size === 0) {
                  delete updated[workspaceId];
                } else {
                  updated[workspaceId] = next;
                }
                return isPermission
                  ? { permissionPendingByWorkspace: updated }
                  : { askUserPendingByWorkspace: updated };
              });
            }
          }
          set({ lastMessage: msg });
        } catch (e) {
          console.error('[NotifyWS] parse error', e);
        }
      };

      ws.onclose = () => {
        clearTimers();
        set({ ws: null, connected: false });
        if (!intentionalClose) {
          set((s) => ({ reconnectCount: s.reconnectCount + 1 }));
          scheduleReconnect();
        }
      };

      ws.onerror = () => ws.close();

      set({ ws });
    },

    disconnect() {
      intentionalClose = true;
      clearTimers();
      const ws = get().ws;
      if (ws) { ws.close(); set({ ws: null, connected: false }); }
    },
  };
});

/**
 * Hook: returns the count of pending permission requests for the given
 * workspace, derived from the cross-page notify channel. Use when rendering
 * a badge from outside the workspace's chat page (where `permission-store`
 * holds the full request list).
 */
export function usePermissionPendingByWorkspace(workspaceId: number): number {
  return useNotificationWSStore((s) => s.permissionPendingByWorkspace[workspaceId]?.size ?? 0);
}

/**
 * Hook: returns the total count of pending permission requests across all
 * workspaces. Used by the global nav to surface "an agent somewhere is
 * waiting for you" without needing a per-workspace render.
 */
export function usePermissionPendingTotal(): number {
  return useNotificationWSStore((s) => {
    let total = 0;
    for (const set of Object.values(s.permissionPendingByWorkspace)) {
      total += set.size;
    }
    return total;
  });
}

/** Hook: pending ask-user-question count for one workspace (cross-page). */
export function useAskUserPendingByWorkspace(workspaceId: number): number {
  return useNotificationWSStore(
    (s) => s.askUserPendingByWorkspace[workspaceId]?.size ?? 0,
  );
}

/** Hook: total pending ask-user-question count across all workspaces. */
export function useAskUserPendingTotal(): number {
  return useNotificationWSStore((s) => {
    let total = 0;
    for (const set of Object.values(s.askUserPendingByWorkspace)) {
      total += set.size;
    }
    return total;
  });
}
