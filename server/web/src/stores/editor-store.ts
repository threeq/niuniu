import { create } from 'zustand';
import type { DiffFile } from '@/lib/hooks/use-diff';

/** crypto.randomUUID() is only available in secure contexts (HTTPS/localhost). */
const generateId = (): string =>
  typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
    ? crypto.randomUUID()
    : 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
        const r = (Math.random() * 16) | 0;
        return (c === 'x' ? r : (r & 0x3) | 0x8).toString(16);
      });

export interface EditorTab {
  id: string;
  type: 'file' | 'diff';
  title: string;
  path: string;
  content?: string;
  diffContent?: string;
}

interface EditorStoreState {
  tabs: EditorTab[];
  activeTabId: string | null;
  openFile: (path: string) => void;
  openDiff: (file: DiffFile) => void;
  closeTab: (id: string) => void;
  setActiveTab: (id: string) => void;
  removeTabsByPath: (path: string) => void;
}

export const useEditorStore = create<EditorStoreState>((set, get) => ({
  tabs: [],
  activeTabId: null,

  openFile: (path: string) => {
    const existing = get().tabs.find((t) => t.path === path && t.type === 'file');
    if (existing) {
      set({ activeTabId: existing.id });
      return;
    }
    const id = generateId();
    const title = path.split('/').pop() || path;
    set((state) => ({
      tabs: [...state.tabs, { id, type: 'file', title, path }],
      activeTabId: id,
    }));
  },

  openDiff: (file: DiffFile) => {
    const existing = get().tabs.find((t) => t.path === file.path && t.type === 'diff');
    if (existing) {
      set({ activeTabId: existing.id });
      return;
    }
    const id = generateId();
    set((state) => ({
      tabs: [...state.tabs, { id, type: 'diff', title: file.path, path: file.path }],
      activeTabId: id,
    }));
  },

  closeTab: (id: string) => {
    set((state) => {
      const newTabs = state.tabs.filter((t) => t.id !== id);
      let newActiveId = state.activeTabId;
      if (state.activeTabId === id) {
        const idx = state.tabs.findIndex((t) => t.id === id);
        newActiveId = newTabs[idx]?.id ?? newTabs[idx - 1]?.id ?? null;
      }
      return { tabs: newTabs, activeTabId: newActiveId };
    });
  },

  setActiveTab: (id: string) => {
    set({ activeTabId: id });
  },

  removeTabsByPath: (path: string) => {
    set((state) => {
      const newTabs = state.tabs.filter((t) => t.path !== path);
      let newActiveId = state.activeTabId;
      if (state.activeTabId !== null && !newTabs.find((t) => t.id === state.activeTabId)) {
        // Active tab was removed, select a nearby tab
        const removedIdx = state.tabs.findIndex((t) => t.id === state.activeTabId);
        newActiveId = newTabs[removedIdx]?.id ?? newTabs[removedIdx - 1]?.id ?? null;
      }
      return { tabs: newTabs, activeTabId: newActiveId };
    });
  },
}));
