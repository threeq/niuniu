// Zustand store for the in-chat ask-user-question UI.
//
// Two transports populate this store, same pattern as permission-store.ts:
//   - REST `load(workspaceId)` — calls `listAskUserRequests`.
//   - SSE `ingestEvent(evt)` — converts the camelCase wire shape from backend
//     OutputEvent into the snake_case AskUserRequest shape so the rest of the
//     SPA treats both transports uniformly.
import { create } from 'zustand';
import type {
  AskUserRequest,
  AskUserRequestStatus,
  AskUserQuestion,
  AskUserDecideBody,
} from '@/types/ask-user';
import { listAskUserRequests, decideAskUser } from '@/lib/api';

// SSE wire shapes (camelCase, matches backend OutputEvent JSON tags).
export type AskUserSSEEvent =
  | {
      type: 'ask_user_request';
      workspaceId: number;
      ts: number;
      askUserRequest: {
        requestId: number;
        sessionId: string;
        questions: AskUserQuestion[];
        requestedAt: number;
        expiresAt: number;
      };
    }
  | {
      type: 'ask_user_decided';
      workspaceId: number;
      ts: number;
      askUserDecided: {
        requestId: number;
        status: AskUserRequestStatus;
        decidedById?: number;
        decisionSource: string;
      };
    };

interface AskUserStoreState {
  byWorkspace: Map<number, AskUserRequest[]>;
  load: (workspaceId: number) => Promise<void>;
  decide: (reqId: number, body: AskUserDecideBody) => Promise<void>;
  ingestEvent: (evt: AskUserSSEEvent) => void;
}

export const useAskUserStore = create<AskUserStoreState>((set, get) => ({
  byWorkspace: new Map(),

  async load(workspaceId) {
    const list = await listAskUserRequests(workspaceId);
    set((state) => {
      const next = new Map(state.byWorkspace);
      next.set(workspaceId, list);
      return { byWorkspace: next };
    });
  },

  async decide(reqId, body) {
    // Optimistic update — flip status to 'answered' locally so the card
    // dismisses without waiting for the round-trip. On error, reload the
    // affected workspace from REST.
    set((state) => {
      const next = new Map(state.byWorkspace);
      for (const [ws, list] of next) {
        const idx = list.findIndex((r) => r.id === reqId);
        if (idx >= 0) {
          const updated = [...list];
          updated[idx] = { ...updated[idx], status: 'answered' };
          next.set(ws, updated);
        }
      }
      return { byWorkspace: next };
    });
    try {
      await decideAskUser(reqId, body);
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
      if (evt.type === 'ask_user_request') {
        const list = next.get(evt.workspaceId) ?? [];
        const r = evt.askUserRequest;
        if (list.some((x) => x.id === r.requestId)) {
          return state;
        }
        next.set(evt.workspaceId, [
          ...list,
          {
            id: r.requestId,
            workspace_id: evt.workspaceId,
            session_id: r.sessionId,
            questions: r.questions,
            requested_at: r.requestedAt,
            expires_at: r.expiresAt,
            status: 'pending',
          },
        ]);
      } else {
        const d = evt.askUserDecided;
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
