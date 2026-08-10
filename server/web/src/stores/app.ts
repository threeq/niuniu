import { create } from 'zustand';
import { persist } from 'zustand/middleware';

interface AppState {
  // 工作空间 tabs
  openWorkspaceTabs: string[];
  activeWorkspaceTab: string | null;
  addWorkspaceTab: (id: string) => void;
  removeWorkspaceTab: (id: string) => void;
  setActiveWorkspaceTab: (id: string) => void;

  // UI 状态
  isSidebarCollapsed: boolean;
  toggleSidebar: () => void;
  setSidebarCollapsed: (collapsed: boolean) => void;

  // 最后打开的项
  lastOpenedProject: string | null;
  lastOpenedWorkspace: string | null;
  lastOpenedRepository: string | null;
  setLastOpenedProject: (id: string) => void;
  setLastOpenedWorkspace: (id: string) => void;
  setLastOpenedRepository: (id: string) => void;
}

export const useAppStore = create<AppState>()(
  persist(
    (set) => ({
  // 工作空间 tabs
  openWorkspaceTabs: [],
  activeWorkspaceTab: null,
  addWorkspaceTab: (id) =>
    set((state) => ({
      openWorkspaceTabs: state.openWorkspaceTabs.includes(id)
        ? state.openWorkspaceTabs
        : [...state.openWorkspaceTabs, id],
      activeWorkspaceTab: id,
    })),
  removeWorkspaceTab: (id) =>
    set((state) => ({
      openWorkspaceTabs: state.openWorkspaceTabs.filter((tabId) => tabId !== id),
      activeWorkspaceTab: state.activeWorkspaceTab === id
        ? state.openWorkspaceTabs.find((tabId) => tabId !== id) || null
        : state.activeWorkspaceTab,
    })),
  setActiveWorkspaceTab: (id) => set({ activeWorkspaceTab: id }),

  // UI 状态
  isSidebarCollapsed: false,
  toggleSidebar: () => set((state) => ({ isSidebarCollapsed: !state.isSidebarCollapsed })),
  setSidebarCollapsed: (collapsed) => set({ isSidebarCollapsed: collapsed }),

  // 最后打开的项
  lastOpenedProject: null,
  lastOpenedWorkspace: null,
  lastOpenedRepository: null,
  setLastOpenedProject: (id) => set({ lastOpenedProject: id }),
  setLastOpenedWorkspace: (id) => set({ lastOpenedWorkspace: id }),
  setLastOpenedRepository: (id) => set({ lastOpenedRepository: id }),
}),
    {
      name: 'niuniu-app-storage',
    }
  )
);
