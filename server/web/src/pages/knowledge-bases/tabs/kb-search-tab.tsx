import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { Search, AlertCircle, FileText } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { EmptyState } from '@/components/shared/empty-state'
import { KBDocumentReader } from '@/components/knowledge/kb-document-reader'
import {
  searchKnowledgeBase,
  type KBDocument,
  type KnowledgeBase,
} from '@/lib/kb-api'

/**
 * Keyword (FTS) search within one KB. Honest about being keyword-, not
 * semantic-matching — the note is deliberate, since "knowledge base" invites the
 * assumption of embeddings.
 *
 * Each hit is clickable and opens the full document, which is what makes search
 * actually useful: a bare snippet rarely carries enough context to judge a match.
 */
export function KBSearchTab({ kb }: { kb: KnowledgeBase }) {
  const { t } = useTranslation('knowledge')
  const [input, setInput] = useState('')
  const [submitted, setSubmitted] = useState('')
  const [reading, setReading] = useState<KBDocument | null>(null)

  const ready = kb.ingest_status === 'ready'

  const { data, isFetching, isError } = useQuery({
    queryKey: ['kb-search', kb.id, submitted],
    queryFn: () => searchKnowledgeBase(kb.id, submitted),
    enabled: ready && submitted.trim().length > 0,
  })

  if (!ready) {
    return (
      <div className="flex h-full items-center justify-center">
        <EmptyState
          icon={<Search className="h-12 w-12 text-muted-foreground" />}
          title={t('browser.notReady')}
          size="compact"
        />
      </div>
    )
  }

  const submit = () => setSubmitted(input.trim())

  return (
    <div className="h-full flex flex-col">
      <div className="flex flex-col gap-1.5 px-4 py-2 border-b">
        <div className="flex items-center gap-2">
          <Input
            value={input}
            placeholder={t('browser.searchPlaceholder')}
            aria-label={t('browser.searchPlaceholder')}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') submit()
            }}
            className="max-w-md"
          />
          <Button
            size="sm"
            variant="outline"
            disabled={input.trim().length === 0 || isFetching}
            onClick={submit}
          >
            <Search className="size-4 mr-1" aria-hidden />
            {isFetching ? t('browser.searching') : t('browser.search')}
          </Button>
        </div>
        <p className="text-xs text-muted-foreground">
          {t('browser.searchKeywordNote')}
        </p>
      </div>

      <div className="flex-1 min-h-0 overflow-y-auto p-4">
        {isError && (
          <p className="flex items-center gap-2 text-sm text-destructive">
            <AlertCircle className="size-4" aria-hidden />
            {t('browser.loadError')}
          </p>
        )}

        {!isError && submitted.trim().length === 0 && (
          <p className="text-sm text-muted-foreground">{t('browser.resultsHint')}</p>
        )}

        {!isError &&
          submitted.trim().length > 0 &&
          !isFetching &&
          (!data || data.length === 0) && (
            <p className="text-sm text-muted-foreground">
              {t('browser.resultsEmpty')}
            </p>
          )}

        {data && data.length > 0 && (
          <ul className="flex flex-col gap-2">
            {data.map((hit, i) => (
              <li key={`${hit.document_id}:${hit.chunk_index}:${i}`}>
                <button
                  onClick={() =>
                    // The hit carries the document's id and path, which is all the
                    // reader needs — no extra lookup to open it.
                    setReading({
                      id: hit.document_id,
                      kb_id: kb.id,
                      path: hit.document_path,
                      title: hit.document_path.split('/').pop() ?? hit.document_path,
                      size: 0,
                      chunk_count: 0,
                    })
                  }
                  className="w-full text-left rounded-md border border-warm-border bg-warm-surface p-2.5 transition-colors hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand"
                >
                  <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
                    <FileText className="size-3.5 shrink-0" aria-hidden />
                    <span className="truncate">
                      {t('browser.hitLocation', {
                        path: hit.document_path,
                        index: hit.chunk_index,
                      })}
                    </span>
                  </span>
                  <span className="block text-sm mt-1 whitespace-pre-wrap break-words">
                    {hit.snippet}
                  </span>
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>

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
