/**
 * In-memory cache of `Workspace` shells keyed by workspace id, written by
 * the detail screen after a successful `GET /workspaces/:id`.
 *
 * Drives cache-first rendering: when the user revisits a workspace, the
 * detail screen reads the cached shell synchronously on mount so the
 * header/menu/input render immediately while a background refresh
 * updates fields that may have changed (status, total_cost, etc.).
 *
 * This is process-local (not persisted): a cold app launch starts with
 * an empty cache and pays the same first-load cost as before. The win
 * is purely intra-session — opening 5 workspaces in a row only pays the
 * full-screen spinner once per workspace, not on every revisit.
 */
import { create } from 'zustand';
import type { Workspace } from '../api/types';

interface WorkspaceDetailState {
  workspaces: Record<string, Workspace>;
  setWorkspace: (id: string, ws: Workspace) => void;
  getWorkspace: (id: string) => Workspace | undefined;
}

export const useWorkspaceDetailStore = create<WorkspaceDetailState>()(
  (set, get) => ({
    workspaces: {},
    setWorkspace: (id, ws) =>
      set((state) => ({
        workspaces: { ...state.workspaces, [id]: ws },
      })),
    getWorkspace: (id) => get().workspaces[id],
  }),
);
