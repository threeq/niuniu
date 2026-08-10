import { create } from 'zustand';
import { toast } from 'sonner';
import i18n from '@/i18n';
import { listPinnedWorkspaces, pinWorkspace, unpinWorkspace } from '@/lib/api';

// Per-user "pinned to top" workspace ids for the sidebar. The server is the
// source of truth (per-user, cross-device sync); a user-namespaced localStorage
// key is kept as a best-effort cache so the pinned zone paints instantly before
// the network round-trip resolves and never leaks pins across accounts in the
// same browser.
//
// Ids are strings (the Workspace id type across the sidebar) and ordered
// most-recently-pinned first, so the pinned zone renders newest pins on top.
const KEY_PREFIX = 'sidebarPinnedWorkspaces';
const keyFor = (uid: number) => `${KEY_PREFIX}:u${uid}`;

function readCache(uid: number): string[] {
  try {
    const raw = localStorage.getItem(keyFor(uid));
    if (!raw) return [];
    const parsed = JSON.parse(raw) as unknown;
    return Array.isArray(parsed) ? parsed.filter((x): x is string => typeof x === 'string') : [];
  } catch {
    return [];
  }
}

function writeCache(uid: number, ids: string[]): void {
  try {
    localStorage.setItem(keyFor(uid), JSON.stringify(ids));
  } catch {
    /* private mode / quota — in-memory state still works this session */
  }
}

interface PinnedWorkspacesState {
  /** The user whose pins are currently loaded (0 = anonymous/unauthenticated). */
  uid: number;
  /** Pinned workspace ids, most-recently-pinned first. */
  pinnedIds: string[];
  /** Load pins for a user; a no-op when the same user is already loaded. */
  loadForUser: (uid: number) => void;
  /** Pin an unpinned workspace (moves it to the front) or unpin a pinned one. */
  toggle: (id: string | number) => void;
}

export const usePinnedWorkspacesStore = create<PinnedWorkspacesState>((set, get) => ({
  uid: 0,
  pinnedIds: [],
  loadForUser: (uid) => {
    if (get().uid === uid) return;
    // Instant paint from cache, then reconcile against the server.
    set({ uid, pinnedIds: readCache(uid) });
    if (uid <= 0) return;
    void listPinnedWorkspaces()
      .then((ids) => {
        // Ignore a stale response if the active user switched mid-flight.
        if (get().uid !== uid) return;
        const strIds = ids.map(String);
        writeCache(uid, strIds);
        set({ pinnedIds: strIds });
      })
      .catch(() => {
        /* offline / error — keep the cached view */
      });
  },
  toggle: (id) => {
    const sid = String(id);
    const { uid, pinnedIds } = get();
    const wasPinned = pinnedIds.includes(sid);
    const next = wasPinned
      ? pinnedIds.filter((x) => x !== sid)
      : [sid, ...pinnedIds];
    // Optimistic update + cache; persist to the server in the background.
    writeCache(uid, next);
    set({ pinnedIds: next });
    if (uid <= 0) return;
    const request = wasPinned ? unpinWorkspace(sid) : pinWorkspace(sid);
    void request.catch(() => {
      // Revert on failure, tolerating concurrent toggles of other cards.
      const cur = get().pinnedIds;
      const reverted = wasPinned
        ? cur.includes(sid)
          ? cur
          : [sid, ...cur]
        : cur.filter((x) => x !== sid);
      writeCache(get().uid, reverted);
      set({ pinnedIds: reverted });
      toast.error(i18n.t('workspaces:sidebar.pinned.failed'));
    });
  },
}));

/** Reactive boolean selector for a single workspace — re-renders a card only
 * when its own pinned state flips, not on every other card's pin change. */
export function useIsPinned(id: string | number): boolean {
  const sid = String(id);
  return usePinnedWorkspacesStore((s) => s.pinnedIds.includes(sid));
}
