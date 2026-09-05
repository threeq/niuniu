import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { it, expect, vi, beforeEach } from 'vitest'
import { KBDocumentsTab } from './kb-documents-tab'
import * as kbApi from '@/lib/kb-api'
import type { KnowledgeBase, KBDocument } from '@/lib/kb-api'

vi.mock('@/lib/kb-api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/kb-api')>()
  return { ...actual, listKBDocuments: vi.fn(), readKBDocument: vi.fn() }
})

function wrap(ui: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const readyKB: KnowledgeBase = {
  id: 1,
  name: '中文古诗词',
  description: '',
  source_kind: 'url',
  source_location: 'https://example.com/poetry.tar.gz',
  status: 'enabled',
  ingest_status: 'ready',
  ingest_progress: 100,
  ingest_error: null,
  doc_count: 250,
  chunk_count: 900,
  last_indexed_at: null,
  bindings: [],
  created_at: '2026-06-30T00:00:00Z',
}

function makeDocs(n: number, offset = 0): KBDocument[] {
  return Array.from({ length: n }, (_, i) => ({
    id: offset + i + 1,
    kb_id: 1,
    path: `tang/poem-${offset + i + 1}.json`,
    title: `poem-${offset + i + 1}.json`,
    size: 1024,
    chunk_count: 2,
  }))
}

beforeEach(() => vi.clearAllMocks())

it('lists a page of documents with the total count', async () => {
  vi.mocked(kbApi.listKBDocuments).mockResolvedValue({
    items: makeDocs(3),
    total: 250,
  })

  wrap(<KBDocumentsTab kb={readyKB} />)

  await waitFor(() =>
    expect(screen.getByText('poem-1.json')).toBeInTheDocument(),
  )
  // "第 1–3 条，共 250 条" — the range tells the user there is more beyond the page.
  expect(screen.getByText(/共 250 条/)).toBeInTheDocument()
})

// The whole point of server-side filtering: a match past the first page must
// still be reachable, so the query has to go to the backend, not filter locally.
it('sends the filter to the server and resets to the first page', async () => {
  vi.mocked(kbApi.listKBDocuments).mockResolvedValue({
    items: makeDocs(100),
    total: 250,
  })

  wrap(<KBDocumentsTab kb={readyKB} />)
  // Wait for the first page to render, not just for the call to be issued: the
  // paging buttons only exist once a total > page size is known.
  await waitFor(() =>
    expect(screen.getByText('poem-1.json')).toBeInTheDocument(),
  )

  // Go to page 2 first, so we can prove the filter resets the offset.
  fireEvent.click(screen.getByRole('button', { name: /下一页/ }))
  await waitFor(() =>
    expect(kbApi.listKBDocuments).toHaveBeenCalledWith(1, {
      q: undefined,
      limit: 100,
      offset: 100,
    }),
  )

  fireEvent.change(screen.getByLabelText('按文件路径筛选…'), {
    target: { value: 'li-bai' },
  })
  fireEvent.click(screen.getByRole('button', { name: '搜索' }))

  await waitFor(() =>
    expect(kbApi.listKBDocuments).toHaveBeenCalledWith(1, {
      q: 'li-bai',
      limit: 100,
      offset: 0,
    }),
  )
})

it('hides paging controls when everything fits on one page', async () => {
  vi.mocked(kbApi.listKBDocuments).mockResolvedValue({
    items: makeDocs(3),
    total: 3,
  })

  wrap(<KBDocumentsTab kb={readyKB} />)
  await waitFor(() =>
    expect(screen.getByText('poem-1.json')).toBeInTheDocument(),
  )
  expect(screen.queryByRole('button', { name: /下一页/ })).toBeNull()
})

it('opens the reader when a document is clicked', async () => {
  vi.mocked(kbApi.listKBDocuments).mockResolvedValue({
    items: makeDocs(1),
    total: 1,
  })
  vi.mocked(kbApi.readKBDocument).mockResolvedValue({
    id: 1,
    kb_id: 1,
    path: 'tang/poem-1.json',
    title: 'poem-1.json',
    content: '床前明月光',
    size: 1024,
    truncated: false,
    extracted: false,
  })

  wrap(<KBDocumentsTab kb={readyKB} />)
  await waitFor(() =>
    expect(screen.getByText('poem-1.json')).toBeInTheDocument(),
  )
  fireEvent.click(screen.getByText('poem-1.json'))

  await waitFor(() =>
    expect(screen.getByText('床前明月光')).toBeInTheDocument(),
  )
})

// Browsing a corpus that has not finished indexing would 404 or return nothing;
// the tab must say so rather than firing the request.
it('does not query while the KB is still ingesting', () => {
  wrap(
    <KBDocumentsTab kb={{ ...readyKB, ingest_status: 'indexing' }} />,
  )
  expect(screen.getByText(/知识库尚未就绪/)).toBeInTheDocument()
  expect(kbApi.listKBDocuments).not.toHaveBeenCalled()
})
