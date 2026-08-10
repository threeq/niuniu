import { render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { it, expect, vi, beforeEach } from 'vitest'
import { KnowledgeBasesPanel } from './knowledge-bases-panel'
import * as kbApi from '@/lib/kb-api'
import type { KnowledgeBase } from '@/lib/kb-api'

// Partial mock: keep the real isKBBusy classifier (the row + panel polling
// depend on it) and stub only the network calls.
vi.mock('@/lib/kb-api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/kb-api')>()
  return {
    ...actual,
    listKnowledgeBases: vi.fn(),
    listKBPresets: vi.fn(),
  }
})

function wrap(ui: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const baseKB: KnowledgeBase = {
  id: 1,
  name: '中文古诗词',
  description: '',
  source_kind: 'url',
  source_location: 'https://example.com/poetry.tar.gz',
  status: 'enabled',
  ingest_status: 'ready',
  ingest_progress: 100,
  ingest_error: null,
  doc_count: 12,
  chunk_count: 340,
  last_indexed_at: null,
  bindings: [],
  created_at: '2026-06-30T00:00:00Z',
}

beforeEach(() => vi.clearAllMocks())

it('renders the empty state when there are no knowledge bases', async () => {
  vi.mocked(kbApi.listKnowledgeBases).mockResolvedValue([])
  wrap(<KnowledgeBasesPanel />)
  await waitFor(() =>
    expect(
      screen.getByText(/还没有知识库/, { exact: false }),
    ).toBeInTheDocument(),
  )
})

it('renders a ready KB with doc/chunk counts and a browse button', async () => {
  vi.mocked(kbApi.listKnowledgeBases).mockResolvedValue([baseKB])
  wrap(<KnowledgeBasesPanel />)
  await waitFor(() => expect(screen.getByText('中文古诗词')).toBeInTheDocument())
  // counts string: "12 个文档 · 340 个分片"
  expect(screen.getByText(/12 个文档/)).toBeInTheDocument()
  expect(screen.getByText(/340 个分片/)).toBeInTheDocument()
  expect(screen.getByRole('button', { name: '浏览' })).toBeInTheDocument()
})

it('shows ingest stage + percent while a KB is downloading', async () => {
  vi.mocked(kbApi.listKnowledgeBases).mockResolvedValue([
    { ...baseKB, ingest_status: 'downloading', ingest_progress: 42 },
  ])
  wrap(<KnowledgeBasesPanel />)
  await waitFor(() => expect(screen.getByText('下载中')).toBeInTheDocument())
  expect(screen.getByText('42%')).toBeInTheDocument()
  // No browse button until the corpus is indexed.
  expect(screen.queryByRole('button', { name: '浏览' })).toBeNull()
})

it('shows a retry button and the error message when ingest failed', async () => {
  vi.mocked(kbApi.listKnowledgeBases).mockResolvedValue([
    {
      ...baseKB,
      ingest_status: 'failed',
      ingest_progress: 0,
      ingest_error: '主源不可达',
    },
  ])
  wrap(<KnowledgeBasesPanel />)
  await waitFor(() =>
    expect(screen.getByRole('button', { name: '重试' })).toBeInTheDocument(),
  )
  expect(screen.getByText('主源不可达')).toBeInTheDocument()
})
