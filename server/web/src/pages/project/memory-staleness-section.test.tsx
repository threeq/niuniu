import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'
import { MemoryStalenessSection } from './memory-staleness-section'
import { memoryApi } from '@/lib/memory-api'
import type { Memory, MemorySweepRun } from '@/types/api'

vi.mock('@/lib/memory-api', () => ({
  memoryApi: {
    listReviewQueue: vi.fn(),
    listSweepRuns: vi.fn(),
    keepMemory: vi.fn(),
    confirmDeleteMemory: vi.fn(),
  },
}))

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

function renderWithQuery(ui: ReactElement) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>)
}

const queued: Memory = {
  id: 7,
  owner_type: 'user',
  owner_id: 0,
  project_id: 1,
  workspace_id: null,
  mem_type: 'gotcha',
  title: 'obsolete anchor memory',
  content: 'old behavior',
  source: 'extract',
  source_path: 'internal/gone.go',
  version: 1,
  pending_review: true,
  stale_reason: 'anchor file no longer exists: internal/gone.go',
  deleted_at: '2026-06-17T00:00:00Z',
  created_at: '2026-06-17T00:00:00Z',
  updated_at: '2026-06-17T00:00:00Z',
}

const run: MemorySweepRun = {
  id: 3,
  project_id: 1,
  trigger: 'schedule',
  scanned: 12,
  auto_deleted: 1,
  queued: 1,
  detail: '{}',
  created_at: '2026-06-17T00:00:00Z',
}

beforeEach(() => {
  vi.mocked(memoryApi.listReviewQueue).mockResolvedValue([queued])
  vi.mocked(memoryApi.listSweepRuns).mockResolvedValue([run])
  vi.mocked(memoryApi.keepMemory).mockResolvedValue(undefined as never)
  vi.mocked(memoryApi.confirmDeleteMemory).mockResolvedValue(undefined as never)
})

afterEach(() => vi.clearAllMocks())

describe('MemoryStalenessSection', () => {
  it('renders the review queue (title + stale reason) and the per-project sweep log', async () => {
    renderWithQuery(<MemoryStalenessSection projectId={1} />)

    await waitFor(() => expect(screen.getByText('obsolete anchor memory')).toBeInTheDocument())
    expect(screen.getByText(/anchor file no longer exists/)).toBeInTheDocument()
    // Both per-project endpoints were queried.
    expect(memoryApi.listReviewQueue).toHaveBeenCalledWith(1)
    expect(memoryApi.listSweepRuns).toHaveBeenCalledWith(1)
  })

  it('keeps a queued memory when the keep action is clicked', async () => {
    renderWithQuery(<MemoryStalenessSection projectId={1} />)
    await waitFor(() => expect(screen.getByText('obsolete anchor memory')).toBeInTheDocument())

    // Keep is the first action button rendered for a queue item.
    fireEvent.click(screen.getAllByRole('button')[0])
    await waitFor(() => expect(memoryApi.keepMemory).toHaveBeenCalledWith(7))
  })

  it('confirms deletion when the delete action is clicked', async () => {
    renderWithQuery(<MemoryStalenessSection projectId={1} />)
    await waitFor(() => expect(screen.getByText('obsolete anchor memory')).toBeInTheDocument())

    fireEvent.click(screen.getAllByRole('button')[1])
    await waitFor(() => expect(memoryApi.confirmDeleteMemory).toHaveBeenCalledWith(7))
  })
})
