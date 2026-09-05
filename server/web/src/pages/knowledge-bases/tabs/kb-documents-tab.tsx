import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery, keepPreviousData } from '@tanstack/react-query'
import {
  FileText,
  Search,
  AlertCircle,
  ChevronLeft,
  ChevronRight,
} from 'lucide-react'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/shared/empty-state'
import { KBDocumentReader } from '@/components/knowledge/kb-document-reader'
import { listKBDocuments, type KBDocument, type KnowledgeBase } from '@/lib/kb-api'

const PAGE_SIZE = 100

/**
 * Browsable, filterable, paged document list — plus an in-app reader.
 *
 * The old browse dialog fetched every document in one unbounded request and could
 * only show each file's name and chunk count; a preset corpus of several thousand
 * files made it both slow and useless. Filtering and paging happen server-side so
 * a match beyond the first page is still findable.
 */
export function KBDocumentsTab({ kb }: { kb: KnowledgeBase }) {
  const { t, i18n } = useTranslation('knowledge')
  const [input, setInput] = useState('')
  const [query, setQuery] = useState('')
  const [page, setPage] = useState(0)
  const [reading, setReading] = useState<KBDocument | null>(null)

  const ready = kb.ingest_status === 'ready'

  const { data, isLoading, isError, isFetching } = useQuery({
    queryKey: ['kb-documents', kb.id, query, page],
    queryFn: () =>
      listKBDocuments(kb.id, {
        q: query || undefined,
        limit: PAGE_SIZE,
        offset: page * PAGE_SIZE,
      }),
    enabled: ready,
    // Keep the previous page visible while the next one loads, so paging doesn't
    // flash an empty list.
    placeholderData: keepPreviousData,
  })

  const submit = () => {
    setQuery(input.trim())
    setPage(0) // a new filter invalidates the current offset
  }

  if (!ready) {
    return (
      <div className="flex h-full items-center justify-center">
        <EmptyState
          icon={<FileText className="h-12 w-12 text-muted-foreground" />}
          title={t('browser.notReady')}
          size="compact"
        />
      </div>
    )
  }

  const total = data?.total ?? 0
  const items = data?.items ?? []
  const from = total === 0 ? 0 : page * PAGE_SIZE + 1
  const to = Math.min(total, (page + 1) * PAGE_SIZE)
  const hasPrev = page > 0
  const hasNext = to < total

  return (
    <div className="h-full flex flex-col">
      <div className="flex items-center gap-2 px-4 py-2 border-b">
        <div className="flex items-center gap-1.5 flex-1 max-w-sm bg-background border rounded-md px-2 py-1">
          <Search className="w-3.5 h-3.5 text-muted-foreground shrink-0" aria-hidden />
          <input
            type="text"
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') submit()
            }}
            placeholder={t('documents.filterPlaceholder')}
            aria-label={t('documents.filterPlaceholder')}
            className="flex-1 text-xs outline-none bg-transparent text-foreground placeholder:text-muted-foreground"
          />
        </div>
        <Button size="sm" variant="outline" onClick={submit} disabled={isFetching}>
          {t('browser.search')}
        </Button>
        <span className="ml-auto text-xs text-muted-foreground tabular-nums">
          {t('documents.range', {
            from: from.toLocaleString(i18n.language),
            to: to.toLocaleString(i18n.language),
            total: total.toLocaleString(i18n.language),
          })}
        </span>
      </div>

      <div className="flex-1 min-h-0 overflow-y-auto">
        {isLoading ? (
          <p className="p-4 text-sm text-muted-foreground">
            {t('browser.documentsLoading')}
          </p>
        ) : isError ? (
          <p className="flex items-center gap-2 p-4 text-sm text-destructive">
            <AlertCircle className="size-4" aria-hidden />
            {t('browser.loadError')}
          </p>
        ) : items.length === 0 ? (
          <p className="p-4 text-sm text-muted-foreground">
            {query ? t('common:status.noMatchingResults') : t('browser.documentsEmpty')}
          </p>
        ) : (
          <ul className="divide-y divide-warm-border/60">
            {items.map((doc) => (
              <li key={doc.id}>
                <button
                  onClick={() => setReading(doc)}
                  className={cn(
                    'w-full flex items-center gap-3 px-4 py-2 text-left transition-colors',
                    'hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand',
                  )}
                >
                  <FileText
                    className="size-4 text-muted-foreground shrink-0"
                    aria-hidden
                  />
                  <span className="flex-1 min-w-0">
                    <span className="block text-sm truncate">
                      {doc.title || doc.path}
                    </span>
                    <span className="block text-xs text-muted-foreground truncate">
                      {doc.path}
                    </span>
                  </span>
                  <span className="shrink-0 text-xs text-muted-foreground tabular-nums">
                    {formatBytes(doc.size, i18n.language)}
                    {' · '}
                    {t('browser.docChunks', { count: doc.chunk_count })}
                  </span>
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>

      {(hasPrev || hasNext) && (
        <div className="flex items-center justify-center gap-2 px-4 py-2 border-t">
          <Button
            size="sm"
            variant="outline"
            disabled={!hasPrev || isFetching}
            onClick={() => setPage((p) => Math.max(0, p - 1))}
          >
            <ChevronLeft className="size-4 mr-1" aria-hidden />
            {t('documents.prev')}
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={!hasNext || isFetching}
            onClick={() => setPage((p) => p + 1)}
          >
            {t('documents.next')}
            <ChevronRight className="size-4 ml-1" aria-hidden />
          </Button>
        </div>
      )}

      <KBDocumentReader
        kbId={kb.id}
        doc={reading}
        onOpenChange={(open) => {
          if (!open) setReading(null)
        }}
      />
    </div>
  )
}

/** Byte size in the reader's locale. Kept local: the only other formatBytes in
 *  the tree lives inside the repositories page and isn't exported. */
function formatBytes(bytes: number, locale: string): string {
  if (bytes < 1024) return `${bytes} B`
  const units = ['KB', 'MB', 'GB']
  let value = bytes / 1024
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${value.toLocaleString(locale, { maximumFractionDigits: 1 })} ${units[unit]}`
}
