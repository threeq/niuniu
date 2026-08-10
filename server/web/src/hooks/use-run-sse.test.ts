import { renderHook, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createElement } from 'react'
import { vi, it, expect, beforeEach } from 'vitest'
import { useRunSSE } from './use-run-sse'

// ---------------------------------------------------------------------------
// Mock agent-sse-store
// ---------------------------------------------------------------------------
const mockAddGlobalHandler = vi.fn()
const mockUnsub = vi.fn()

vi.mock('@/stores/agent-sse-store', () => ({
  useAgentSSEStore: {
    getState: () => ({
      addGlobalHandler: mockAddGlobalHandler,
    }),
  },
}))

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------
function makeWrapper(qc: QueryClient) {
  return function Wrapper({ children }: { children: React.ReactNode }) {
    return createElement(QueryClientProvider, { client: qc }, children)
  }
}

function setup(qc: QueryClient) {
  return renderHook(() => useRunSSE({ workspaceId: 1, projectId: 2 }), {
    wrapper: makeWrapper(qc),
  })
}

beforeEach(() => {
  mockAddGlobalHandler.mockReset()
  mockUnsub.mockReset()
  mockAddGlobalHandler.mockReturnValue(mockUnsub)
})

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

it('registers a global handler on mount and unsubscribes on unmount', () => {
  const qc = new QueryClient()
  const { unmount } = setup(qc)

  expect(mockAddGlobalHandler).toHaveBeenCalledTimes(1)
  unmount()
  expect(mockUnsub).toHaveBeenCalledTimes(1)
})

it('routes gate_started → setQueryData with passed = 0', () => {
  let capturedHandler: ((msg: unknown) => void) | null = null
  mockAddGlobalHandler.mockImplementation((fn: (msg: unknown) => void) => {
    capturedHandler = fn
    return mockUnsub
  })

  const qc = new QueryClient()
  setup(qc)

  expect(capturedHandler).not.toBeNull()

  act(() => {
    capturedHandler!({
      type: 'gate_started',
      gateJob: { runId: 7, jobId: 1, columnId: 3, specCount: 4 },
    })
  })

  expect(qc.getQueryData(['gate-progress', 7])).toEqual({
    jobId: 1,
    columnId: 3,
    specCount: 4,
    passed: 0,
  })
})

it('routes gate_progress → increments passed via Math.max', () => {
  let capturedHandler: ((msg: unknown) => void) | null = null
  mockAddGlobalHandler.mockImplementation((fn: (msg: unknown) => void) => {
    capturedHandler = fn
    return mockUnsub
  })

  const qc = new QueryClient()
  setup(qc)

  // Seed existing cache
  qc.setQueryData(['gate-progress', 7], { jobId: 1, columnId: 3, specCount: 4, passed: 0 })

  act(() => {
    capturedHandler!({ type: 'gate_progress', gateProgress: { jobId: 1, runId: 7, specId: 10, index: 2, total: 4, passed: true } })
  })

  expect((qc.getQueryData(['gate-progress', 7]) as { passed: number }).passed).toBe(2)
})

it('routes gate_done → removes gate-progress cache key', () => {
  let capturedHandler: ((msg: unknown) => void) | null = null
  mockAddGlobalHandler.mockImplementation((fn: (msg: unknown) => void) => {
    capturedHandler = fn
    return mockUnsub
  })

  const qc = new QueryClient()
  setup(qc)

  qc.setQueryData(['gate-progress', 7], { jobId: 1, columnId: 3, specCount: 4, passed: 3 })

  act(() => {
    capturedHandler!({ type: 'gate_done', gateDone: { jobId: 1, runId: 7, passed: true, failureCount: 0, durationMs: 500 } })
  })

  expect(qc.getQueryData(['gate-progress', 7])).toBeUndefined()
})

it('routes run_phase_started → invalidates issues and workspace-active-run', () => {
  let capturedHandler: ((msg: unknown) => void) | null = null
  mockAddGlobalHandler.mockImplementation((fn: (msg: unknown) => void) => {
    capturedHandler = fn
    return mockUnsub
  })

  const qc = new QueryClient()
  const invalidateSpy = vi.spyOn(qc, 'invalidateQueries')
  setup(qc)

  act(() => {
    capturedHandler!({ type: 'run_phase_started', runPhase: { runId: 5, columnId: 2, columnName: 'Spec' } })
  })

  expect(invalidateSpy).toHaveBeenCalledWith(expect.objectContaining({ queryKey: ['issues', { projectId: 2 }] }))
  expect(invalidateSpy).toHaveBeenCalledWith(expect.objectContaining({ queryKey: ['workspace-active-run', 1] }))
})

it('routes agent_lifecycle → invalidates workspace-agents', () => {
  let capturedHandler: ((msg: unknown) => void) | null = null
  mockAddGlobalHandler.mockImplementation((fn: (msg: unknown) => void) => {
    capturedHandler = fn
    return mockUnsub
  })

  const qc = new QueryClient()
  const invalidateSpy = vi.spyOn(qc, 'invalidateQueries')
  setup(qc)

  act(() => {
    capturedHandler!({ type: 'agent_lifecycle', agentLifecycle: { runId: 5, workspaceId: 1, action: 'start_for_column', status: 'ok' } })
  })

  expect(invalidateSpy).toHaveBeenCalledWith(expect.objectContaining({ queryKey: ['workspace-agents', 1] }))
})

it('skips issues invalidation when projectId is 0', () => {
  let capturedHandler: ((msg: unknown) => void) | null = null
  mockAddGlobalHandler.mockImplementation((fn: (msg: unknown) => void) => {
    capturedHandler = fn
    return mockUnsub
  })

  const qc = new QueryClient()
  const invalidateSpy = vi.spyOn(qc, 'invalidateQueries')

  renderHook(() => useRunSSE({ workspaceId: 1, projectId: 0 }), {
    wrapper: makeWrapper(qc),
  })

  act(() => {
    capturedHandler!({ type: 'run_phase_started', runPhase: { runId: 5, columnId: 2, columnName: 'Spec' } })
  })

  // Should NOT invalidate issues
  const issueCalls = invalidateSpy.mock.calls.filter(
    ([opts]) => Array.isArray((opts as { queryKey?: unknown[] }).queryKey) &&
      (opts as { queryKey: unknown[] }).queryKey[0] === 'issues',
  )
  expect(issueCalls).toHaveLength(0)
  // But workspace-active-run should still be invalidated
  expect(invalidateSpy).toHaveBeenCalledWith(expect.objectContaining({ queryKey: ['workspace-active-run', 1] }))
})
