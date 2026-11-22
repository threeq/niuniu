import { api, listPermissionRequests } from '@/lib/api';
import { useNotificationWSStore } from '@/stores/notification-ws-store';
import type { Workspace } from '@/types/api';

let inFlight: Promise<void> | null = null;

/**
 * Seed `permissionPendingByWorkspace` from REST so pending authorization
 * requests survive a reload. The notify WS only delivers events emitted after
 * the socket is open; without this backfill, sessions already blocked on a
 * permission prompt would be missing from the sidebar's needs-authorization
 * group (and its per-card badge) until a new event arrived.
 *
 * Completed/archived sessions cannot have a pending request, so they are
 * skipped to keep the fan-out small. Concurrent callers share one run.
 */
export function backfillPendingPermissions(): Promise<void> {
  if (inFlight) return inFlight;
  inFlight = (async () => {
    try {
      const workspaces = await api.get<Workspace[]>('/workspaces');
      const active = workspaces.filter(
        (ws) => ws.is_archived !== 1 && ws.status !== 'completed',
      );
      await Promise.allSettled(
        active.map(async (ws) => {
          const id = Number(ws.id);
          if (!Number.isFinite(id)) return;
          const requests = await listPermissionRequests(id);
          useNotificationWSStore.getState().seedPermissionPending(
            id,
            requests.filter((r) => r.status === 'pending').map((r) => r.id),
          );
        }),
      );
    } catch {
      // Best effort: live notify events still populate the map.
    } finally {
      inFlight = null;
    }
  })();
  return inFlight;
}
