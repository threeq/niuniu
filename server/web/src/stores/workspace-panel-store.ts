import { create } from 'zustand';
import { persist } from 'zustand/middleware';

// Toggleable right-side panels. Canvas/drawio are intentionally NOT here: they
// are no longer standalone panels — a `.excalidraw`/`.drawio` file opens in the
// central content viewer instead (see ContentViewerTarget below).
export type PanelId = 'chat' | 'files' | 'changes' | 'terminal' | 'issue' | 'pinned' | 'artifact';

/**
 * The central content viewer sits between the chat and the right-side panels.
 * It shows exactly one thing at a time — a file, a git diff, or a diagram —
 * replacing the old full-screen file modal and the changes-panel focus mode.
 */
export type ContentViewerTarget =
  | { kind: 'file'; path: string; title?: string }
  | { kind: 'diff'; repo: string; path: string }
  // Autohost 安全网: one file's diff inside a hidden-ref checkpoint step. checkpointId
  // is the per-repo checkpoint row id; path selects the file within that step's diff.
  | { kind: 'checkpoint-diff'; checkpointId: number; path: string; repoName: string; step: number }
  | { kind: 'canvas'; path: string }
  | { kind: 'drawio'; path: string };

// Map a workspace-relative file path to its content-viewer target, dispatching
// diagrams to their editor by extension. Shared by the file tree and the
// artifacts list so clicking either opens content the same way.
export function contentTargetForPath(path: string, title?: string): ContentViewerTarget {
  const p = path.toLowerCase();
  if (p.endsWith('.excalidraw')) return { kind: 'canvas', path };
  if (p.endsWith('.drawio')) return { kind: 'drawio', path };
  return { kind: 'file', path, title };
}

interface WorkspacePanelState {
  openPanels: PanelId[];
  togglePanel: (panel: PanelId) => void;
  isPanelOpen: (panel: PanelId) => boolean;

  isSidebarOpen: boolean;
  toggleSidebar: () => void;
  // Transient (non-persisted): the viewer auto-hid the sidebar, so closing the
  // viewer should restore it. Cleared once the user toggles the sidebar manually.
  sidebarHiddenByViewer: boolean;

  // Central content viewer target, per workspace. `null` (or absent) = hidden.
  contentViewer: Record<string, ContentViewerTarget | null>;
  getContentViewer: (workspaceId: string) => ContentViewerTarget | null;
  // Open a target in the viewer. Auto-collapses the sidebar for a wider reading
  // area (remembering that we were the one to hide it, so closing restores it).
  openContentViewer: (workspaceId: string, target: ContentViewerTarget) => void;
  closeContentViewer: (workspaceId: string) => void;

  // File-list path filter per workspace (Changes panel). Stored here (not local)
  // so it survives remounts.
  changesFileSearch: Record<string, string>;
  setChangesFileSearch: (workspaceId: string, query: string) => void;

  sidebarWidth: number;
  setSidebarWidth: (width: number) => void;

  activeWorktreeTab: Record<string, string>;
  setActiveWorktreeTab: (workspaceId: string, worktreeName: string) => void;
  getActiveWorktreeTab: (workspaceId: string) => string | undefined;
}

const VALID_PANELS: ReadonlySet<string> = new Set([
  'chat',
  'files',
  'changes',
  'terminal',
  'issue',
  'pinned',
  'artifact',
]);

// Compare two viewer targets for identity (used to toggle-off on re-click).
function sameTarget(a: ContentViewerTarget | null, b: ContentViewerTarget): boolean {
  if (!a || a.kind !== b.kind) return false;
  if (a.kind === 'diff' && b.kind === 'diff') return a.repo === b.repo && a.path === b.path;
  if (a.kind === 'checkpoint-diff' && b.kind === 'checkpoint-diff')
    return a.checkpointId === b.checkpointId && a.path === b.path;
  return 'path' in a && 'path' in b && a.path === b.path;
}

export const useWorkspacePanelStore = create<WorkspacePanelState>()(
  persist(
    (set, get) => ({
      openPanels: ['chat'],

      togglePanel: (panel) => {
        if (panel === 'chat') return; // chat is always open
        set((state) => ({
          openPanels: state.openPanels.includes(panel)
            ? state.openPanels.filter((p) => p !== panel)
            : [...state.openPanels, panel],
        }));
      },

      isPanelOpen: (panel) => get().openPanels.includes(panel),

      isSidebarOpen: true,
      toggleSidebar: () =>
        set((state) => ({
          isSidebarOpen: !state.isSidebarOpen,
          // A manual toggle clears the "auto-hidden by viewer" flag: once the
          // user drives the sidebar themselves, closing the viewer shouldn't
          // yank it back.
          sidebarHiddenByViewer: false,
        })),

      // Non-persisted transient flag: did the content viewer hide the sidebar?
      sidebarHiddenByViewer: false,

      contentViewer: {},
      getContentViewer: (workspaceId) => get().contentViewer[workspaceId] ?? null,
      openContentViewer: (workspaceId, target) =>
        set((state) => {
          const current = state.contentViewer[workspaceId] ?? null;
          // Re-clicking the open target closes the viewer.
          if (sameTarget(current, target)) {
            return {
              contentViewer: { ...state.contentViewer, [workspaceId]: null },
              isSidebarOpen: state.sidebarHiddenByViewer ? true : state.isSidebarOpen,
              sidebarHiddenByViewer: false,
            };
          }
          const hideSidebar = state.isSidebarOpen;
          return {
            contentViewer: { ...state.contentViewer, [workspaceId]: target },
            isSidebarOpen: hideSidebar ? false : state.isSidebarOpen,
            sidebarHiddenByViewer: hideSidebar || state.sidebarHiddenByViewer,
          };
        }),
      closeContentViewer: (workspaceId) =>
        set((state) => ({
          contentViewer: { ...state.contentViewer, [workspaceId]: null },
          isSidebarOpen: state.sidebarHiddenByViewer ? true : state.isSidebarOpen,
          sidebarHiddenByViewer: false,
        })),

      changesFileSearch: {},
      setChangesFileSearch: (workspaceId, query) =>
        set((state) => ({
          changesFileSearch: { ...state.changesFileSearch, [workspaceId]: query },
        })),

      sidebarWidth: 300,
      setSidebarWidth: (width) => {
        const clamped = Math.max(200, Math.min(480, Math.round(width)));
        set({ sidebarWidth: clamped });
      },

      activeWorktreeTab: {},
      setActiveWorktreeTab: (workspaceId, worktreeName) =>
        set((state) => ({
          activeWorktreeTab: {
            ...state.activeWorktreeTab,
            [workspaceId]: worktreeName,
          },
        })),
      getActiveWorktreeTab: (workspaceId) => get().activeWorktreeTab[workspaceId],
    }),
    {
      name: 'niuniu-workspace-panels',
      version: 3,
      // Transient viewer state is never persisted — a reload starts with the
      // viewer closed and the sidebar restored.
      partialize: (state) => {
        const { contentViewer: _cv, sidebarHiddenByViewer: _shv, ...rest } =
          state as WorkspacePanelState & { sidebarHiddenByViewer: boolean };
        return rest;
      },
      migrate: (persisted, _version) => {
        const state = (persisted ?? {}) as Partial<WorkspacePanelState>;
        const openPanels = Array.isArray(state.openPanels)
          ? (state.openPanels.filter((p) => VALID_PANELS.has(p as string)) as PanelId[])
          : (['chat'] as PanelId[]);
        return { ...state, openPanels } as WorkspacePanelState;
      },
    }
  )
);
