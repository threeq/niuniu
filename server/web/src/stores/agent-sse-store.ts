import { create } from 'zustand';
import { getAccessToken } from '@/stores/auth-store';
import {
  usePermissionStore,
  type PermissionSSEEvent,
} from '@/stores/permission-store';
import {
  useAskUserStore,
  type AskUserSSEEvent,
} from '@/stores/ask-user-store';
import type { AgentMessage } from '@/types/api';

/** crypto.randomUUID() is only available in secure contexts (HTTPS/localhost). */
const generateId = (): string =>
  typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
    ? crypto.randomUUID()
    : 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
        const r = (Math.random() * 16) | 0;
        return (c === 'x' ? r : (r & 0x3) | 0x8).toString(16);
      });

type MessageHandler = (msg: AgentMessage) => void;

interface AgentSSEState {
  windowId: string;
  es: EventSource | null;
  // All workspace IDs currently subscribed
  subscribedWorkspaces: Set<string>;
  // Per-workspace message handlers (multiple can coexist)
  handlers: Map<string, Set<MessageHandler>>;
  // Global handlers that receive ALL messages regardless of workspace
  globalHandlers: Set<MessageHandler>;
  // Last event timestamp for replay on reconnect
  lastEventTs: number;
  // Whether the SSE is intentionally disconnected (browser closing)
  intentionalClose: boolean;
  // Callbacks to invoke on reconnect
  onReconnectCallbacks: Set<() => void>;

  /** Initialize SSE connection with given workspace IDs */
  connect: (workspaceIds: string[]) => void;
  /** Add a workspace to the subscription (without disconnecting others) */
  addWorkspace: (workspaceId: string) => void;
  /** Remove a workspace from the subscription */
  removeWorkspace: (workspaceId: string) => void;
  /** Register a per-workspace message handler. Returns unsubscribe function. */
  addHandler: (workspaceId: string, fn: MessageHandler) => () => void;
  /** Register a global handler for ALL workspaces. Returns unsubscribe function. */
  addGlobalHandler: (fn: MessageHandler) => () => void;
  /** Register a reconnect callback. Returns unsubscribe function. */
  addOnReconnect: (fn: () => void) => () => void;
  /** Disconnect SSE entirely */
  disconnect: () => void;
}

function getOrCreateWindowId(): string {
  let id = sessionStorage.getItem('niuniu:windowId');
  if (!id) {
    id = generateId();
    sessionStorage.setItem('niuniu:windowId', id);
  }
  return id;
}

export const useAgentSSEStore = create<AgentSSEState>((set, get) => ({
  windowId: getOrCreateWindowId(),
  es: null,
  subscribedWorkspaces: new Set<string>(),
  handlers: new Map<string, Set<MessageHandler>>(),
  globalHandlers: new Set<MessageHandler>(),
  lastEventTs: 0,
  intentionalClose: false,
  onReconnectCallbacks: new Set<() => void>(),

  connect(workspaceIds) {
    const state = get();

    // Close existing connection
    if (state.es) {
      state.es.close();
    }

    if (workspaceIds.length === 0) {
      set({ es: null, subscribedWorkspaces: new Set() });
      return;
    }

    const subscribed = new Set(workspaceIds);
    set({ subscribedWorkspaces: subscribed, intentionalClose: false });

    const windowId = get().windowId;
    const lastTs = get().lastEventTs;

    const token = getAccessToken();
    let url = `/ws/sse?windowId=${encodeURIComponent(windowId)}&workspaces=${workspaceIds.join(',')}`;
    if (token) {
      url += `&token=${encodeURIComponent(token)}`;
    }
    if (lastTs > 0) {
      url += `&since=${lastTs}`;
    }

    const es = new EventSource(url);

    es.onopen = () => {
      console.info('[SSE] CONNECTED', { windowId, workspaces: workspaceIds, since: lastTs || 'initial' });
      if (lastTs > 0) {
        // This is a reconnect, not initial connect
        get().onReconnectCallbacks.forEach(fn => fn());
      }
    };

    es.onerror = () => {
      const currentEs = get().es;
      if (currentEs !== es || get().intentionalClose) return;

      set({ es: null });
      // Auto-reconnect with all current workspaces
      setTimeout(() => {
        const ws = Array.from(get().subscribedWorkspaces);
        if (ws.length > 0 && !get().intentionalClose) {
          console.info('[SSE] reconnecting...', { workspaces: ws });
          get().connect(ws);
        }
      }, 2000);
    };

    es.onmessage = (event) => {
      try {
        const msg: AgentMessage = JSON.parse(event.data);
        if (msg.type === 'ping') return;

        // Per-workspace 403: server signals this workspace_id is unauthorized.
        // Log and silently drop it from the subscription without killing the stream.
        if (msg.type === 'workspace_auth_error') {
          const wsId = String(msg.workspaceId);
          console.warn('[SSE] per-workspace auth error — dropping workspace from subscription', wsId);
          const { subscribedWorkspaces } = get();
          if (subscribedWorkspaces.has(wsId)) {
            const newSet = new Set(subscribedWorkspaces);
            newSet.delete(wsId);
            set({ subscribedWorkspaces: newSet });
          }
          return;
        }

        // Track timestamp for replay
        if (msg.ts) {
          set({ lastEventTs: msg.ts });
        }

        // Permission-prompt events feed the permission store directly so the
        // in-chat dialog stays in sync regardless of which components are
        // currently mounted. Wire shape is camelCase per backend OutputEvent.
        if (msg.type === 'permission_request' || msg.type === 'permission_decided') {
          usePermissionStore.getState().ingestEvent(
            msg as unknown as PermissionSSEEvent,
          );
        }

        // Ask-user-question events follow the same pattern as permission.
        if (msg.type === 'ask_user_request' || msg.type === 'ask_user_decided') {
          useAskUserStore.getState().ingestEvent(
            msg as unknown as AskUserSSEEvent,
          );
        }

        // Route to the correct workspace handlers
        const wsId = String(msg.workspaceId);
        const handlerSet = get().handlers.get(wsId);
        if (handlerSet) {
          handlerSet.forEach((handler) => handler(msg));
        }

        // Route to global handlers (all workspaces)
        get().globalHandlers.forEach((handler) => handler(msg));
      } catch (e) {
        console.error('[SSE] parse error', e);
      }
    };

    set({ es });
  },

  addWorkspace(workspaceId) {
    const { subscribedWorkspaces } = get();
    if (subscribedWorkspaces.has(workspaceId)) return;

    const newSet = new Set(subscribedWorkspaces);
    newSet.add(workspaceId);
    set({ subscribedWorkspaces: newSet });

    // Reconnect with expanded workspace list
    get().connect(Array.from(newSet));
  },

  removeWorkspace(workspaceId) {
    const { subscribedWorkspaces } = get();
    if (!subscribedWorkspaces.has(workspaceId)) return;

    const newSet = new Set(subscribedWorkspaces);
    newSet.delete(workspaceId);
    set({ subscribedWorkspaces: newSet });

    if (newSet.size === 0) {
      get().disconnect();
    } else {
      get().connect(Array.from(newSet));
    }
  },

  addHandler(workspaceId, fn) {
    const handlers = new Map(get().handlers);
    const existingSet = handlers.get(workspaceId) ?? new Set<MessageHandler>();
    const newSet = new Set(existingSet);
    newSet.add(fn);
    handlers.set(workspaceId, newSet);
    set({ handlers });

    // Ensure this workspace is subscribed and SSE connection is alive
    const { subscribedWorkspaces, es } = get();
    if (!subscribedWorkspaces.has(workspaceId)) {
      get().addWorkspace(workspaceId);
    } else if (!es || es.readyState === EventSource.CLOSED) {
      // Workspace is subscribed but EventSource is gone — reconnect
      get().connect(Array.from(subscribedWorkspaces));
    }

    // Return unsubscribe function — removes only this specific handler
    return () => {
      const current = get().handlers;
      const currentSet = current.get(workspaceId);
      if (currentSet?.has(fn)) {
        const updated = new Map(current);
        const updatedSet = new Set(currentSet);
        updatedSet.delete(fn);
        if (updatedSet.size === 0) {
          updated.delete(workspaceId);
        } else {
          updated.set(workspaceId, updatedSet);
        }
        set({ handlers: updated });
      }
    };
  },

  addGlobalHandler(fn) {
    const updated = new Set(get().globalHandlers);
    updated.add(fn);
    set({ globalHandlers: updated });
    return () => {
      const current = new Set(get().globalHandlers);
      current.delete(fn);
      set({ globalHandlers: current });
    };
  },

  addOnReconnect(fn) {
    const callbacks = new Set(get().onReconnectCallbacks);
    callbacks.add(fn);
    set({ onReconnectCallbacks: callbacks });
    return () => {
      const current = new Set(get().onReconnectCallbacks);
      current.delete(fn);
      set({ onReconnectCallbacks: current });
    };
  },

  disconnect() {
    set({ intentionalClose: true });
    const { es } = get();
    if (es) {
      es.close();
      set({ es: null });
    }
  },
}));
