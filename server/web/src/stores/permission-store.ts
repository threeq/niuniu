// Zustand store for the in-chat permission-prompt UI.
//
// Two transports populate this store:
//   - REST `load(workspaceId)` — calls `listPermissionRequests` which already
//     returns snake_case PermissionRequest objects with unix-ms timestamps
//     (per Task 13 / server/internal/api/permission.go).
//   - SSE `ingestEvent(evt)` — converts the camelCase wire shape emitted by
//     backend OutputEvent (server/internal/event/types.go) into the snake_case
//     PermissionRequest shape so the rest of the SPA can treat both transports
//     uniformly.
//
// The store is keyed by workspace ID; each value is the list of requests known
// for that workspace (pending and recently-decided alike — UI filters as it
// likes).
import { create } from 'zustand';
import type {
  PermissionRequest,
  PermissionRequestStatus,
  DecideBody,
} from '@/types/permission';
import { listPermissionRequests, decidePermission } from '@/lib/api';

// SSE wire shapes (camelCase, matches backend OutputEvent JSON tags).
// These are deliberately separate from the REST PermissionRequest type:
// REST returns snake_case, SSE returns camelCase, the store normalises to
// snake_case internally.
export type PermissionSSEEvent =
  | {
      type: 'permission_request';
      workspaceId: number;
      ts: number;
      permissionRequest: {
        requestId: number;
        sessionId: string;
        toolName: string;
        toolInput: Record<string, unknown>;
        requestedAt: number;
        expiresAt: number;
      };
    }
  | {
      type: 'permission_decided';
      workspaceId: number;
      ts: number;
      permissionDecided: {
        requestId: number;
        status: PermissionRequestStatus;
        decidedById?: number;
        decisionSource: string;
      };
    };

interface PermissionStoreState {
  byWorkspace: Map<number, PermissionRequest[]>;
  load: (workspaceId: number) => Promise<void>;
  decide: (reqId: number, body: DecideBody) => Promise<void>;
  ingestEvent: (evt: PermissionSSEEvent) => void;
}

export const usePermissionStore = create<PermissionStoreState>((set, get) => ({
  byWorkspace: new Map(),

  async load(workspaceId) {
    const list = await listPermissionRequests(workspaceId);
    set((state) => {
      const next = new Map(state.byWorkspace);
      next.set(workspaceId, list);
      return { byWorkspace: next };
    });
  },

  async decide(reqId, body) {
    // Optimistic update — flip status locally so the dialog dismisses without
    // waiting for the round-trip. On error, reload the affected workspace from
    // REST to recover authoritative state.
    set((state) => {
      const next = new Map(state.byWorkspace);
      for (const [ws, list] of next) {
        const idx = list.findIndex((r) => r.id === reqId);
        if (idx >= 0) {
          const updated = [...list];
          updated[idx] = {
            ...updated[idx],
            status: body.decision === 'allow' ? 'allowed' : 'denied',
          };
          next.set(ws, updated);
        }
      }
      return { byWorkspace: next };
    });
    try {
      await decidePermission(reqId, body);
    } catch (e) {
      const ws = [...get().byWorkspace.keys()].find((k) =>
        (get().byWorkspace.get(k) ?? []).some((r) => r.id === reqId),
      );
      if (ws !== undefined) await get().load(ws);
      throw e;
    }
  },

  ingestEvent(evt) {
    set((state) => {
      const next = new Map(state.byWorkspace);
      if (evt.type === 'permission_request') {
        const list = next.get(evt.workspaceId) ?? [];
        const r = evt.permissionRequest;
        // Dedupe — backend MAY redeliver after reconnect; treat same id as
        // a no-op rather than duplicating the row.
        if (list.some((x) => x.id === r.requestId)) {
          return state;
        }
        next.set(evt.workspaceId, [
          ...list,
          {
            id: r.requestId,
            workspace_id: evt.workspaceId,
            session_id: r.sessionId,
            tool_name: r.toolName,
            tool_input: r.toolInput,
            requested_at: r.requestedAt,
            expires_at: r.expiresAt,
            status: 'pending',
          },
        ]);
      } else {
        const d = evt.permissionDecided;
        const list = next.get(evt.workspaceId) ?? [];
        next.set(
          evt.workspaceId,
          list.map((r) =>
            r.id === d.requestId ? { ...r, status: d.status } : r,
          ),
        );
      }
      return { byWorkspace: next };
    });
  },
}));
