import { describe, it, expect, beforeEach } from 'vitest';
import { useEditorStore } from './editor-store';

describe('editorStore', () => {
  beforeEach(() => {
    // Reset store state before each test
    useEditorStore.setState({ tabs: [], activeTabId: null });
  });
  it('should open a file tab', () => {
    useEditorStore.getState().openFile('/src/app.ts');
    const tabs = useEditorStore.getState().tabs;
    expect(tabs).toHaveLength(1);
    expect(tabs[0].type).toBe('file');
    expect(tabs[0].path).toBe('/src/app.ts');
  });

  it('should open a diff tab', () => {
    useEditorStore.getState().openDiff({
      path: 'src/app.ts',
      status: 'modified',
      additions: 5,
      deletions: 2,
      hunks: [],
    });
    const tabs = useEditorStore.getState().tabs;
    expect(tabs).toHaveLength(1);
    expect(tabs[0].type).toBe('diff');
  });

  it('should close a tab', () => {
    useEditorStore.getState().openFile('/src/app.ts');
    const tabId = useEditorStore.getState().tabs[0].id;
    useEditorStore.getState().closeTab(tabId);
    expect(useEditorStore.getState().tabs).toHaveLength(0);
  });

  it('should set active tab', () => {
    useEditorStore.getState().openFile('/src/a.ts');
    useEditorStore.getState().openFile('/src/b.ts');
    const tabB = useEditorStore.getState().tabs[1].id;
    useEditorStore.getState().setActiveTab(tabB);
    expect(useEditorStore.getState().activeTabId).toBe(tabB);
  });

  it('should not open duplicate tabs for same path', () => {
    useEditorStore.getState().openFile('/src/app.ts');
    const firstTabId = useEditorStore.getState().tabs[0].id;
    useEditorStore.getState().openFile('/src/app.ts');
    expect(useEditorStore.getState().tabs).toHaveLength(1);
    expect(useEditorStore.getState().activeTabId).toBe(firstTabId);
  });
});
