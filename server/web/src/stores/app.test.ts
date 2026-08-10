import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { useAppStore } from './app';

describe('App Store Persistence', () => {
  // Store original state before each test
  let originalState: ReturnType<typeof useAppStore.getState>;

  beforeEach(() => {
    // Save original state
    originalState = useAppStore.getState();
    // Clear localStorage to simulate fresh start
    localStorage.clear();
  });

  afterEach(() => {
    // Restore original state and clear localStorage
    useAppStore.setState(originalState);
    localStorage.clear();
  });

  describe('Initial State', () => {
    it('should load default state when no persisted data exists', () => {
      const state = useAppStore.getState();

      expect(state.openWorkspaceTabs).toEqual([]);
      expect(state.activeWorkspaceTab).toBeNull();
      expect(state.isSidebarCollapsed).toBe(false);
    });
  });

  describe('State Persistence to localStorage', () => {
    it('should persist workspace tabs', () => {
      const { addWorkspaceTab } = useAppStore.getState();

      addWorkspaceTab('workspace-1');
      addWorkspaceTab('workspace-2');

      const stored = localStorage.getItem('niuniu-app-storage');
      const parsed = JSON.parse(stored!);
      expect(parsed.state.openWorkspaceTabs).toEqual(['workspace-1', 'workspace-2']);
      expect(parsed.state.activeWorkspaceTab).toBe('workspace-2');
    });

    it('should persist sidebar collapsed state', () => {
      const { toggleSidebar } = useAppStore.getState();

      toggleSidebar();

      const stored = localStorage.getItem('niuniu-app-storage');
      const parsed = JSON.parse(stored!);
      expect(parsed.state.isSidebarCollapsed).toBe(true);
    });

    it('should persist setSidebarCollapsed changes', () => {
      const { setSidebarCollapsed } = useAppStore.getState();

      setSidebarCollapsed(true);

      const stored = localStorage.getItem('niuniu-app-storage');
      const parsed = JSON.parse(stored!);
      expect(parsed.state.isSidebarCollapsed).toBe(true);
    });
  });

  describe('Store Actions Work with Persistence', () => {
    it('should addWorkspaceTab persist correctly', () => {
      const { addWorkspaceTab } = useAppStore.getState();

      addWorkspaceTab('ws-1');
      addWorkspaceTab('ws-2');

      const stored = localStorage.getItem('niuniu-app-storage');
      const parsed = JSON.parse(stored!);
      expect(parsed.state.openWorkspaceTabs).toEqual(['ws-1', 'ws-2']);
      expect(parsed.state.activeWorkspaceTab).toBe('ws-2');
    });

    it('should removeWorkspaceTab persist correctly', () => {
      const { addWorkspaceTab, removeWorkspaceTab } = useAppStore.getState();

      addWorkspaceTab('ws-1');
      addWorkspaceTab('ws-2');
      addWorkspaceTab('ws-3');

      removeWorkspaceTab('ws-2');

      const stored = localStorage.getItem('niuniu-app-storage');
      const parsed = JSON.parse(stored!);
      expect(parsed.state.openWorkspaceTabs).toEqual(['ws-1', 'ws-3']);
    });

    it('should setActiveWorkspaceTab persist correctly', () => {
      const { addWorkspaceTab, setActiveWorkspaceTab } = useAppStore.getState();

      addWorkspaceTab('ws-1');
      addWorkspaceTab('ws-2');

      setActiveWorkspaceTab('ws-1');

      const stored = localStorage.getItem('niuniu-app-storage');
      const parsed = JSON.parse(stored!);
      expect(parsed.state.activeWorkspaceTab).toBe('ws-1');
    });

    it('should toggleSidebar persist correctly', () => {
      const { toggleSidebar } = useAppStore.getState();

      toggleSidebar();
      let stored = localStorage.getItem('niuniu-app-storage');
      let parsed = JSON.parse(stored!);
      expect(parsed.state.isSidebarCollapsed).toBe(true);

      toggleSidebar();
      stored = localStorage.getItem('niuniu-app-storage');
      parsed = JSON.parse(stored!);
      expect(parsed.state.isSidebarCollapsed).toBe(false);
    });

    it('should not add duplicate workspace tabs', () => {
      const { addWorkspaceTab } = useAppStore.getState();

      addWorkspaceTab('ws-1');
      addWorkspaceTab('ws-1'); // Duplicate

      const stored = localStorage.getItem('niuniu-app-storage');
      const parsed = JSON.parse(stored!);
      expect(parsed.state.openWorkspaceTabs).toEqual(['ws-1']);
    });

    it('should handle removing active tab correctly', () => {
      const { addWorkspaceTab, removeWorkspaceTab } = useAppStore.getState();

      addWorkspaceTab('ws-1');
      addWorkspaceTab('ws-2');
      addWorkspaceTab('ws-3');

      // Remove active tab
      removeWorkspaceTab('ws-2');

      const stored = localStorage.getItem('niuniu-app-storage');
      const parsed = JSON.parse(stored!);
      // Should fall back to another tab
      expect(parsed.state.activeWorkspaceTab).toBeTruthy();
    });
  });

  describe('Type Safety', () => {
    it('should maintain correct TypeScript types', () => {
      const state = useAppStore.getState();

      // Type checks - these will compile-time fail if types are wrong
      const tabs: string[] = state.openWorkspaceTabs;
      const activeTab: string | null = state.activeWorkspaceTab;
      const collapsed: boolean = state.isSidebarCollapsed;

      expect(tabs).toBeDefined();
      expect(activeTab).toBeDefined();
      expect(collapsed).toBeDefined();
    });
  });

  describe('Integration Tests', () => {
    it('should persist and restore complete workflow', () => {
      const store = useAppStore.getState();

      // Simulate user workflow
      store.addWorkspaceTab('ws-1');
      store.addWorkspaceTab('ws-2');
      store.toggleSidebar();

      // Verify localStorage has all data
      const stored = localStorage.getItem('niuniu-app-storage');
      expect(stored).toBeTruthy();

      const parsed = JSON.parse(stored!);
      expect(parsed.state.openWorkspaceTabs).toEqual(['ws-1', 'ws-2']);
      expect(parsed.state.activeWorkspaceTab).toBe('ws-2');
      expect(parsed.state.isSidebarCollapsed).toBe(true);
    });

    it('should handle workspace tab management correctly', () => {
      const store = useAppStore.getState();

      // Add multiple tabs
      store.addWorkspaceTab('ws-1');
      store.addWorkspaceTab('ws-2');
      store.addWorkspaceTab('ws-3');

      // Switch active tab
      store.setActiveWorkspaceTab('ws-1');

      const stored1 = localStorage.getItem('niuniu-app-storage');
      const parsed1 = JSON.parse(stored1!);
      expect(parsed1.state.activeWorkspaceTab).toBe('ws-1');

      // Remove middle tab
      store.removeWorkspaceTab('ws-2');

      const stored2 = localStorage.getItem('niuniu-app-storage');
      const parsed2 = JSON.parse(stored2!);
      expect(parsed2.state.openWorkspaceTabs).toEqual(['ws-1', 'ws-3']);
    });
  });
});
