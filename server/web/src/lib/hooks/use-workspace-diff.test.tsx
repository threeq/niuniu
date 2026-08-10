import { renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createElement, type ReactNode } from 'react';
import { vi, it, expect, describe, beforeEach } from 'vitest';

// Mock api.get to serve seeded responses by URL — declared before importing the
// hook so the vi.mock factory is in place.
const getMock = vi.fn();
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>();
  return { ...actual, api: { ...actual.api, get: (url: string) => getMock(url) } };
});

import { useWorkspaceDiff } from './use-workspace-diff';

function wrapper(qc: QueryClient) {
  return function W({ children }: { children: ReactNode }) {
    return createElement(QueryClientProvider, { client: qc }, children);
  };
}

const file = (path: string, additions: number, deletions: number, status = 'modified') => ({
  path,
  status,
  additions,
  deletions,
  hunks: [],
  raw_patch: `patch-${path}`,
});

beforeEach(() => {
  getMock.mockReset();
  getMock.mockImplementation((url: string) => {
    if (url.endsWith('/diff')) {
      return Promise.resolve([
        // Out of name order on purpose (to assert sorting).
        {
          name: 'zebra',
          repository_id: 5,
          worktree_path: '/w/zebra',
          base_branch: 'main',
          current_branch: 'ws/zebra',
          files: [file('a.ts', 2, 1)],
        },
        {
          name: 'alpha',
          repository_id: 0, // unresolved → orphan
          worktree_path: '/w/alpha',
          base_branch: 'develop',
          current_branch: 'ws/alpha', // orphan still carries its branch (from diff row)
          files: [file('b.ts', 5, 0, 'added')],
        },
      ]);
    }
    if (url.endsWith('/comments')) {
      return Promise.resolve([{ id: 1, repo: 'zebra', file_path: 'a.ts' }]);
    }
    // GET /workspaces/:id  → workspace (ahead_count joined by repo name)
    return Promise.resolve({
      worktrees: [{ repo_name: 'zebra', branch: 'x', base_branch: 'main', ahead_count: 3 }],
    });
  });
});

describe('useWorkspaceDiff', () => {
  function render() {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return renderHook(() => useWorkspaceDiff('42'), { wrapper: wrapper(qc) });
  }

  it('maps repository_id to repoId, nulling unresolved (0) groups', async () => {
    const { result } = render();
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    await waitFor(() => expect(result.current.repos).toHaveLength(2));

    const byName = Object.fromEntries(result.current.repos.map((r) => [r.name, r]));
    expect(byName.zebra.repoId).toBe('5');
    expect(byName.alpha.repoId).toBeNull();
  });

  it('retains rawFiles only for unresolved (orphan) groups', async () => {
    const { result } = render();
    await waitFor(() => expect(result.current.repos).toHaveLength(2));

    const byName = Object.fromEntries(result.current.repos.map((r) => [r.name, r]));
    // Resolved group: rawFiles dropped (it re-fetches line-level by id).
    expect(byName.zebra.rawFiles).toHaveLength(0);
    // Orphan group: rawFiles kept (inline line-level rendering, incl. raw_patch).
    expect(byName.alpha.rawFiles).toHaveLength(1);
    expect(byName.alpha.rawFiles[0].raw_patch).toBe('patch-b.ts');
  });

  it('sorts groups by name and attaches comment counts + ahead count', async () => {
    const { result } = render();
    await waitFor(() => expect(result.current.repos).toHaveLength(2));

    expect(result.current.repos.map((r) => r.name)).toEqual(['alpha', 'zebra']);

    const zebra = result.current.repos.find((r) => r.name === 'zebra')!;
    expect(zebra.files[0].commentCount).toBe(1); // comment on zebra/a.ts
    expect(zebra.aheadCount).toBe(3); // joined from workspace.worktrees by name
    expect(zebra.currentBranch).toBe('ws/zebra'); // sourced directly from the diff row

    const alpha = result.current.repos.find((r) => r.name === 'alpha')!;
    expect(alpha.files[0].commentCount).toBe(0);
    expect(alpha.aheadCount).toBe(0); // orphan name not in worktrees → 0
    expect(alpha.currentBranch).toBe('ws/alpha'); // orphan still carries its branch from the diff row
  });

  it('computes totals and distinct base branches', async () => {
    const { result } = render();
    await waitFor(() => expect(result.current.repos).toHaveLength(2));

    expect(result.current.totalFiles).toBe(2);
    expect(result.current.totalAdditions).toBe(7); // 2 + 5
    expect(result.current.totalDeletions).toBe(1); // 1 + 0
    expect([...result.current.baseBranches].sort()).toEqual(['develop', 'main']);
  });
});
