import { describe, it, expect, beforeEach, vi } from 'vitest';

vi.mock('@/lib/api', () => ({
  listPinnedWorkspaces: vi.fn(),
  pinWorkspace: vi.fn(),
  unpinWorkspace: vi.fn(),
}));
vi.mock('@/i18n', () => ({ default: { t: (k: string) => k } }));
vi.mock('sonner', () => ({ toast: { error: vi.fn() } }));

import { usePinnedWorkspacesStore } from './pinned-workspaces-store';
import { listPinnedWorkspaces, pinWorkspace, unpinWorkspace } from '@/lib/api';
import { toast } from 'sonner';

const mockList = vi.mocked(listPinnedWorkspaces);
const mockPin = vi.mocked(pinWorkspace);
const mockUnpin = vi.mocked(unpinWorkspace);

const keyFor = (uid: number) => `sidebarPinnedWorkspaces:u${uid}`;
const flush = () => new Promise((r) => setTimeout(r, 0));

function resetStore() {
  // Force loadForUser to re-run by clearing the loaded uid first.
  usePinnedWorkspacesStore.setState({ uid: -1, pinnedIds: [] });
}

describe('pinned-workspaces-store', () => {
  beforeEach(() => {
    localStorage.clear();
    resetStore();
    vi.clearAllMocks();
    mockPin.mockResolvedValue(undefined);
    mockUnpin.mockResolvedValue(undefined);
    // Default: no server pins. Individual tests override as needed.
    mockList.mockResolvedValue([]);
  });

  it('optimistically toggles a workspace on and off and calls the server', () => {
    const { loadForUser, toggle } = usePinnedWorkspacesStore.getState();
    loadForUser(1);
    toggle('42');
    expect(usePinnedWorkspacesStore.getState().pinnedIds).toEqual(['42']);
    expect(mockPin).toHaveBeenCalledWith('42');
    toggle('42');
    expect(usePinnedWorkspacesStore.getState().pinnedIds).toEqual([]);
    expect(mockUnpin).toHaveBeenCalledWith('42');
  });

  it('puts the most-recently-pinned id first', () => {
    const { loadForUser, toggle } = usePinnedWorkspacesStore.getState();
    loadForUser(1);
    toggle('1');
    toggle('2');
    toggle('3');
    expect(usePinnedWorkspacesStore.getState().pinnedIds).toEqual(['3', '2', '1']);
  });

  it('normalizes numeric ids to strings', () => {
    const { loadForUser, toggle } = usePinnedWorkspacesStore.getState();
    loadForUser(1);
    toggle(7);
    expect(usePinnedWorkspacesStore.getState().pinnedIds).toEqual(['7']);
    expect(mockPin).toHaveBeenCalledWith('7');
  });

  it('writes an optimistic user-namespaced cache', () => {
    const { loadForUser, toggle } = usePinnedWorkspacesStore.getState();
    loadForUser(5);
    toggle('9');
    expect(JSON.parse(localStorage.getItem(keyFor(5))!)).toEqual(['9']);
    expect(localStorage.getItem(keyFor(1))).toBeNull();
  });

  it('seeds instantly from cache, then reconciles with the server response', async () => {
    localStorage.setItem(keyFor(1), JSON.stringify(['a', 'b']));
    mockList.mockResolvedValueOnce([3, 2]);
    usePinnedWorkspacesStore.getState().loadForUser(1);
    // Instant cache seed before the network resolves.
    expect(usePinnedWorkspacesStore.getState().pinnedIds).toEqual(['a', 'b']);
    await flush();
    // Server is the source of truth: ids replaced (and stringified) + recached.
    expect(usePinnedWorkspacesStore.getState().pinnedIds).toEqual(['3', '2']);
    expect(JSON.parse(localStorage.getItem(keyFor(1))!)).toEqual(['3', '2']);
  });

  it('keeps the cached view when the server list fetch fails', async () => {
    localStorage.setItem(keyFor(1), JSON.stringify(['a']));
    mockList.mockRejectedValueOnce(new Error('offline'));
    usePinnedWorkspacesStore.getState().loadForUser(1);
    expect(usePinnedWorkspacesStore.getState().pinnedIds).toEqual(['a']);
    await flush();
    expect(usePinnedWorkspacesStore.getState().pinnedIds).toEqual(['a']);
  });

  it('loads a different user\'s pins without leaking the previous user\'s', () => {
    localStorage.setItem(keyFor(1), JSON.stringify(['a', 'b']));
    localStorage.setItem(keyFor(2), JSON.stringify(['c']));
    // Keep the cached seed stable (skip server reconciliation for this test).
    mockList.mockRejectedValue(new Error('offline'));
    const { loadForUser } = usePinnedWorkspacesStore.getState();
    loadForUser(1);
    expect(usePinnedWorkspacesStore.getState().pinnedIds).toEqual(['a', 'b']);
    loadForUser(2);
    expect(usePinnedWorkspacesStore.getState().pinnedIds).toEqual(['c']);
  });

  it('ignores malformed persisted cache', () => {
    localStorage.setItem(keyFor(1), '{not json');
    mockList.mockRejectedValue(new Error('offline'));
    usePinnedWorkspacesStore.getState().loadForUser(1);
    expect(usePinnedWorkspacesStore.getState().pinnedIds).toEqual([]);
  });

  it('reverts the optimistic toggle and toasts when the server rejects', async () => {
    mockPin.mockRejectedValueOnce(new Error('boom'));
    const { loadForUser, toggle } = usePinnedWorkspacesStore.getState();
    loadForUser(1);
    toggle('42');
    expect(usePinnedWorkspacesStore.getState().pinnedIds).toEqual(['42']);
    await flush();
    expect(usePinnedWorkspacesStore.getState().pinnedIds).toEqual([]);
    expect(toast.error).toHaveBeenCalled();
  });

  it('does not hit the server for an unauthenticated user', async () => {
    const { loadForUser, toggle } = usePinnedWorkspacesStore.getState();
    loadForUser(0);
    toggle('1');
    expect(usePinnedWorkspacesStore.getState().pinnedIds).toEqual(['1']);
    await flush();
    expect(mockList).not.toHaveBeenCalled();
    expect(mockPin).not.toHaveBeenCalled();
  });
});
