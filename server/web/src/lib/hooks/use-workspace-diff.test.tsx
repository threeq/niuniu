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

// Mirrors the backend git.FileDiff shape: structured hunks, no raw_patch — the
// client has no unified-diff parser, so hunks are the only renderable payload.
const file = (path: string, additions: number, deletions: number, status = 'modified') => ({
  path,
  status,
  additions,
  deletions,
  hunks: [
    {
      old_start: 1,
      old_count: 1,
      new_start: 1,
      new_count: 1,
      lines: [{ type: 'add' as const, content: `line in ${path}`, new_line: 1 }],
    },
  ],
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
    // Orphan group: rawFiles kept, carrying the structured hunks the inline
    // line-level viewer renders from.
    expect(byName.alpha.rawFiles).toHaveLength(1);
    expect(byName.alpha.rawFiles[0].hunks[0].lines[0].content).toBe('line in b.ts');
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

  // The file badge counts the review VERDICT, not how many comments were ever
  // written. Counting resolved ones would keep a settled file demanding
  // attention, which is the conflation this wave exists to remove.
  it('counts only unresolved comments in commentCount, keeping the total separately', async () => {
    getMock.mockImplementation((url: string) => {
      if (url.endsWith('/diff')) {
        return Promise.resolve([
          { name: 'zebra', repository_id: 5, files: [file('a.ts', 1, 0)] },
        ]);
      }
      if (url.endsWith('/comments')) {
        return Promise.resolve([
          // Sent to the agent but NOT judged — still open.
          { id: 1, repo: 'zebra', file_path: 'a.ts', sent_to_agent: true, resolved: false },
          // Judged — settled, must not inflate the badge.
          { id: 2, repo: 'zebra', file_path: 'a.ts', sent_to_agent: true, resolved: true },
          // Never sent, never judged — open.
          { id: 3, repo: 'zebra', file_path: 'a.ts', resolved: false },
        ]);
      }
      return Promise.resolve({ worktrees: [] });
    });

    const { result } = render();
    await waitFor(() => expect(result.current.repos).toHaveLength(1));
    const row = result.current.repos[0].files[0];
    expect(row.commentCount).toBe(2);
    expect(row.totalCommentCount).toBe(3);
  });

  it('reports a fully-resolved file as zero open but non-zero total', async () => {
    getMock.mockImplementation((url: string) => {
      if (url.endsWith('/diff')) {
        return Promise.resolve([
          { name: 'zebra', repository_id: 5, files: [file('a.ts', 1, 0)] },
        ]);
      }
      if (url.endsWith('/comments')) {
        return Promise.resolve([
          { id: 1, repo: 'zebra', file_path: 'a.ts', resolved: true },
          { id: 2, repo: 'zebra', file_path: 'a.ts', resolved: true },
        ]);
      }
      return Promise.resolve({ worktrees: [] });
    });

    const { result } = render();
    await waitFor(() => expect(result.current.repos).toHaveLength(1));
    const row = result.current.repos[0].files[0];
    // The two must differ — collapsing them would make "reviewed and settled"
    // indistinguishable from "never looked at".
    expect(row.commentCount).toBe(0);
    expect(row.totalCommentCount).toBe(2);
  });
});
