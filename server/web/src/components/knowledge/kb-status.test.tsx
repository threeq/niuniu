import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { it, expect, vi, beforeEach } from 'vitest'
import { KBStatusBadge, KBStatusDot } from './kb-status'
import { KBDocumentReader } from './kb-document-reader'
import * as kbApi from '@/lib/kb-api'
import type { KnowledgeBase, KBDocument } from '@/lib/kb-api'

// Partial mock: keep the real isKBBusy classifier (the badge derives its state
// from it) and stub only the network calls.
vi.mock('@/lib/kb-api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/kb-api')>()
  return { ...actual, readKBDocument: vi.fn() }
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

// --- status badge -----------------------------------------------------------

it('shows the ready state for an indexed, enabled KB', () => {
  wrap(<KBStatusBadge kb={baseKB} />)
  expect(screen.getByText('就绪')).toBeInTheDocument()
})

it('shows the ingest stage while a KB is downloading', () => {
  wrap(<KBStatusBadge kb={{ ...baseKB, ingest_status: 'downloading' }} />)
  expect(screen.getByText('下载中')).toBeInTheDocument()
})

// Failure must win over "disabled": a KB that failed to ingest is broken, and
// reporting it as merely switched off would hide the actionable state.
it('prefers the failed state over disabled', () => {
  wrap(
    <KBStatusBadge
      kb={{ ...baseKB, status: 'disabled', ingest_status: 'failed' }}
    />,
  )
  expect(screen.getByText('失败')).toBeInTheDocument()
  expect(screen.queryByText('已停用')).toBeNull()
})

it('shows disabled for a ready-but-switched-off KB', () => {
  wrap(<KBStatusBadge kb={{ ...baseKB, status: 'disabled' }} />)
  expect(screen.getByText('已停用')).toBeInTheDocument()
})

// The dot conveys state by color, so it must carry an accessible name too.
it('gives the compact dot an accessible label', () => {
  wrap(<KBStatusDot kb={{ ...baseKB, ingest_status: 'failed' }} />)
  expect(screen.getByRole('img', { name: '失败' })).toBeInTheDocument()
})

// --- document reader --------------------------------------------------------

const doc: KBDocument = {
  id: 7,
  kb_id: 1,
  path: 'tang/li-bai.json',
  title: 'li-bai.json',
  size: 2048,
  chunk_count: 3,
}

it('renders a document’s text in the reader', async () => {
  vi.mocked(kbApi.readKBDocument).mockResolvedValue({
    id: 7,
    kb_id: 1,
    path: doc.path,
    title: doc.title,
    content: '床前明月光',
    size: 2048,
    truncated: false,
    extracted: false,
  })

  wrap(<KBDocumentReader kbId={1} doc={doc} onOpenChange={() => {}} />)

  await waitFor(() =>
    expect(screen.getByText('床前明月光')).toBeInTheDocument(),
  )
  expect(kbApi.readKBDocument).toHaveBeenCalledWith(1, 7)
})

// Both flags are honesty markers: the user must know when text came from an
// extractor, and when they are only seeing the head of a long file.
it('flags extracted and truncated content', async () => {
  vi.mocked(kbApi.readKBDocument).mockResolvedValue({
    id: 7,
    kb_id: 1,
    path: 'report.pdf',
    title: 'report.pdf',
    content: 'extracted text',
    size: 9_000_000,
    truncated: true,
    extracted: true,
  })

  wrap(
    <KBDocumentReader
      kbId={1}
      doc={{ ...doc, path: 'report.pdf', title: 'report.pdf' }}
      onOpenChange={() => {}}
    />,
  )

  await waitFor(() =>
    expect(screen.getByText('已从文档中提取文本')).toBeInTheDocument(),
  )
  expect(screen.getByText('内容较长，仅显示开头部分')).toBeInTheDocument()
})

it('surfaces a read failure instead of rendering an empty document', async () => {
  vi.mocked(kbApi.readKBDocument).mockRejectedValue(
    new Error('document is no longer available'),
  )

  wrap(<KBDocumentReader kbId={1} doc={doc} onOpenChange={() => {}} />)

  await waitFor(() =>
    expect(
      screen.getByText(/document is no longer available/),
    ).toBeInTheDocument(),
  )
})

it('does not fetch until a document is selected', () => {
  wrap(<KBDocumentReader kbId={1} doc={null} onOpenChange={() => {}} />)
  expect(kbApi.readKBDocument).not.toHaveBeenCalled()
})

it('closes via onOpenChange', async () => {
  vi.mocked(kbApi.readKBDocument).mockResolvedValue({
    id: 7,
    kb_id: 1,
    path: doc.path,
    title: doc.title,
    content: 'body',
    size: 10,
    truncated: false,
    extracted: false,
  })
  const onOpenChange = vi.fn()
  wrap(<KBDocumentReader kbId={1} doc={doc} onOpenChange={onOpenChange} />)

  await waitFor(() => expect(screen.getByText('body')).toBeInTheDocument())
  fireEvent.click(screen.getByRole('button', { name: /close/i }))
  expect(onOpenChange).toHaveBeenCalledWith(false)
})
