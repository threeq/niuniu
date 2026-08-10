import { renderHook, act } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createElement, type ReactNode } from 'react';
import { vi, it, expect, describe, beforeEach, afterEach } from 'vitest';
import { ApiError } from '@/lib/api';
import type { Workspace } from '@/types/api';

// ---------------------------------------------------------------------------
// Mocks — declared BEFORE importing the hook under test. vi.mock is hoisted
// but the factory captures references defined here.
// ---------------------------------------------------------------------------
const successMock = vi.fn();
const errorMock = vi.fn();
const warningMock = vi.fn();
const infoMock = vi.fn();

vi.mock('sonner', () => ({
  toast: {
    success: (msg: string, opts?: unknown) => successMock(msg, opts),
    error: (msg: string, opts?: unknown) => errorMock(msg, opts),
    warning: (msg: string, opts?: unknown) => warningMock(msg, opts),
    info: (msg: string, opts?: unknown) => infoMock(msg, opts),
  },
}));

const markWorkspaceDoneMock = vi.fn();
const unmarkWorkspaceDoneMock = vi.fn();
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>();
  return {
    ...actual,
    markWorkspaceDone: (id: number | string) => markWorkspaceDoneMock(id),
    unmarkWorkspaceDone: (id: number | string, prev: string) =>
      unmarkWorkspaceDoneMock(id, prev),
  };
});

// Import AFTER mocks are registered.
import { useMarkWorkspaceDone } from './use-workspaces';

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------
function makeWrapper(qc: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return createElement(QueryClientProvider, { client: qc }, children);
  };
}

function seedWorkspaces(
  qc: QueryClient,
  scope: 'me' | 'all',
  rows: Array<{ id: string; status: string }>,
) {
  qc.setQueryData<Workspace[]>(['workspaces', scope], rows as Workspace[]);
}

function setup(qc: QueryClient, scope: 'mine' | 'all' = 'mine') {
  return renderHook(({ s }: { s: 'mine' | 'all' }) => useMarkWorkspaceDone(s), {
    wrapper: makeWrapper(qc),
    initialProps: { s: scope },
  });
}

// Flush a few microtask queues so chained `await` inside the hook resolves.
async function flushMicrotasks(n = 4) {
  for (let i = 0; i < n; i++) await Promise.resolve();
}

// ---------------------------------------------------------------------------
// Setup
// ---------------------------------------------------------------------------
beforeEach(() => {
  successMock.mockReset();
  errorMock.mockReset();
  warningMock.mockReset();
  infoMock.mockReset();
  markWorkspaceDoneMock.mockReset();
  unmarkWorkspaceDoneMock.mockReset();
  markWorkspaceDoneMock.mockResolvedValue({ status: 'ok' });
  unmarkWorkspaceDoneMock.mockResolvedValue({ status: 'ok' });
});

afterEach(() => {
  vi.useRealTimers();
});

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('useMarkWorkspaceDone', () => {
  it('happy path: trigger → fetch fires immediately, cache shows completed, success toast', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    seedWorkspaces(qc, 'me', [{ id: '7', status: 'needs_review' }]);

    const { result } = setup(qc);

    await act(async () => {
      void result.current.trigger(7);
      await flushMicrotasks();
    });

    expect(markWorkspaceDoneMock).toHaveBeenCalledTimes(1);
    expect(markWorkspaceDoneMock).toHaveBeenCalledWith('7');
    expect(qc.getQueryData<Workspace[]>(['workspaces', 'me'])?.[0].status).toBe('completed');
    expect(successMock).toHaveBeenCalledTimes(1);
    expect(errorMock).not.toHaveBeenCalled();
    expect(warningMock).not.toHaveBeenCalled();
    expect(result.current.pendingIds.has('7')).toBe(false);
  });

  it('undo: clicking the undo action fires unmark API with snapshot status', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    seedWorkspaces(qc, 'me', [{ id: '7', status: 'needs_review' }]);

    const { result } = setup(qc);

    await act(async () => {
      void result.current.trigger(7);
      await flushMicrotasks();
    });

    const opts = successMock.mock.calls[0][1] as {
      action: { onClick: () => void };
    };
    await act(async () => {
      opts.action.onClick();
      await flushMicrotasks();
    });

    expect(unmarkWorkspaceDoneMock).toHaveBeenCalledTimes(1);
    expect(unmarkWorkspaceDoneMock).toHaveBeenCalledWith('7', 'needs_review');
    expect(qc.getQueryData<Workspace[]>(['workspaces', 'me'])?.[0].status).toBe('needs_review');
  });

  it('undo failure with 409: rolls forward to completed and shows stale toast', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    seedWorkspaces(qc, 'me', [{ id: '7', status: 'needs_review' }]);
    unmarkWorkspaceDoneMock.mockRejectedValueOnce(
      new ApiError(409, 'not completed', { error: { code: 'WORKSPACE_NOT_COMPLETED' } }),
    );

    const { result } = setup(qc);

    await act(async () => {
      void result.current.trigger(7);
      await flushMicrotasks();
    });

    const opts = successMock.mock.calls[0][1] as {
      action: { onClick: () => void };
    };
    await act(async () => {
      opts.action.onClick();
      await flushMicrotasks();
    });

    expect(unmarkWorkspaceDoneMock).toHaveBeenCalledTimes(1);
    expect(errorMock).toHaveBeenCalledTimes(1);
    // Stale: cache re-patched to 'completed' since server still thinks so (or moved past it).
    expect(qc.getQueryData<Workspace[]>(['workspaces', 'me'])?.[0].status).toBe('completed');
  });

  it('409 on mark-done (running): error toast and cache rolled back', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    seedWorkspaces(qc, 'me', [{ id: '7', status: 'running' }]);
    markWorkspaceDoneMock.mockRejectedValueOnce(
      new ApiError(409, 'agent running', { error: { code: 'workspace_running' } }),
    );

    const { result } = setup(qc);

    await act(async () => {
      void result.current.trigger(7);
      await flushMicrotasks();
    });

    expect(markWorkspaceDoneMock).toHaveBeenCalledTimes(1);
    expect(errorMock).toHaveBeenCalledTimes(1);
    expect(qc.getQueryData<Workspace[]>(['workspaces', 'me'])?.[0].status).toBe('running');
    expect(result.current.pendingIds.has('7')).toBe(false);
  });

  it('dedupe: a second trigger while the first is in flight does not re-fire', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    seedWorkspaces(qc, 'me', [{ id: '7', status: 'needs_review' }]);
    // Hold the first call open so we can fire a second before it resolves.
    let resolveFirst: (v: unknown) => void = () => {};
    markWorkspaceDoneMock.mockImplementationOnce(
      () => new Promise((res) => { resolveFirst = res; }),
    );

    const { result } = setup(qc);

    await act(async () => {
      void result.current.trigger(7);
      await flushMicrotasks();
    });

    // Second trigger while the first promise is unresolved.
    await act(async () => {
      void result.current.trigger(7);
      await flushMicrotasks();
    });

    expect(markWorkspaceDoneMock).toHaveBeenCalledTimes(1);
    expect(result.current.pendingIds.has('7')).toBe(true);

    await act(async () => {
      resolveFirst({ status: 'ok' });
      await flushMicrotasks();
    });

    expect(result.current.pendingIds.has('7')).toBe(false);
  });

  it('exposes pendingIds set so callers can disable per-workspace buttons', () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    seedWorkspaces(qc, 'me', [
      { id: '7', status: 'needs_review' },
      { id: '8', status: 'needs_review' },
    ]);

    const { result } = setup(qc);
    expect(result.current.pendingIds).toBeInstanceOf(Set);
    expect(result.current.pendingIds.size).toBe(0);
  });

  it('partial success: response with warnings fires warning toast in addition to success', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    seedWorkspaces(qc, 'me', [{ id: '7', status: 'needs_review' }]);
    markWorkspaceDoneMock.mockResolvedValueOnce({
      status: 'ok',
      warnings: ['issue_lifecycle_sync_failed'],
    });

    const { result } = setup(qc);

    await act(async () => {
      void result.current.trigger(7);
      await flushMicrotasks();
    });

    expect(successMock).toHaveBeenCalledTimes(1);
    expect(warningMock).toHaveBeenCalledTimes(1);
    expect(qc.getQueryData<Workspace[]>(['workspaces', 'me'])?.[0].status).toBe('completed');
  });
});
