import { create } from 'zustand';
import { api } from '@/lib/api';

export interface QuickAction {
  id: number;
  label: string;
  content: string;
  auto_send: boolean;
  position: number;
  owner?: { type: string; id: number; name?: string; slug?: string };
}

/**
 * A quick action contributed by the workspace's active scene(s). Parsed live
 * from the scene projection and never persisted, so it has no DB id (the scene
 * `slug` is its identity) and is read-only in the management UI.
 */
export interface SceneQuickAction {
  slug: string;
  label: string;
  content: string;
  auto_send: boolean;
  source: 'scene';
}

interface WorkspaceQuickActionsResponse {
  owner: QuickAction[];
  scene: SceneQuickAction[];
}

interface QuickActionsState {
  actions: QuickAction[];
  sceneActions: SceneQuickAction[];
  loading: boolean;
  /**
   * Fetch quick actions. When `workspaceId` is given, use the workspace-scoped
   * endpoint that also returns the scene-provided group; otherwise fall back to
   * the global owner-only list.
   */
  fetchActions: (workspaceId?: number) => Promise<void>;
  addAction: (action: { label: string; content: string; auto_send: boolean; owner?: { type: string; id: number } }) => Promise<void>;
  updateAction: (id: number, action: { label: string; content: string; auto_send: boolean }) => Promise<void>;
  removeAction: (id: number) => Promise<void>;
  setLocalOrder: (actions: QuickAction[]) => void;
  persistOrder: () => Promise<void>;
}

export const useQuickActionsStore = create<QuickActionsState>()((set, get) => ({
  actions: [],
  sceneActions: [],
  loading: false,
  fetchActions: async (workspaceId?: number) => {
    set({ loading: true });
    try {
      if (workspaceId) {
        const res = await api.get<WorkspaceQuickActionsResponse>(`/workspaces/${workspaceId}/quick-actions`);
        set({ actions: res.owner ?? [], sceneActions: res.scene ?? [] });
      } else {
        const actions = await api.get<QuickAction[]>('/quick-actions');
        set({ actions, sceneActions: [] });
      }
    } catch {
      // api.get already shows toast on error
    } finally {
      set({ loading: false });
    }
  },
  addAction: async (action) => {
    const created = await api.post<QuickAction>('/quick-actions', action);
    set((state) => ({ actions: [...state.actions, created] }));
  },
  updateAction: async (id, action) => {
    const updated = await api.put<QuickAction>(`/quick-actions/${id}`, action);
    set((state) => ({
      actions: state.actions.map((a) => (a.id === id ? updated : a)),
    }));
  },
  removeAction: async (id) => {
    await api.delete(`/quick-actions/${id}`);
    set((state) => ({
      actions: state.actions.filter((a) => a.id !== id),
    }));
  },
  setLocalOrder: (actions) => {
    set({ actions: actions.map((a, i) => ({ ...a, position: i })) });
  },
  persistOrder: async () => {
    const prevActions = [...get().actions];
    const ids = prevActions.map((a) => a.id);
    try {
      await api.post('/quick-actions/reorder', { ids });
    } catch {
      // Rollback on failure
      set({ actions: prevActions });
    }
  },
}));
