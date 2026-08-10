import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { FileText, Search, AlertCircle } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import {
  listKBDocuments,
  searchKnowledgeBase,
  type KnowledgeBase,
} from '@/lib/kb-api'

interface Props {
  kb: KnowledgeBase | null
  onOpenChange: (open: boolean) => void
}

// In-UI browse + keyword search for one KB. Documents tab lists the source
// files; Search tab runs the FTS endpoint (honest: keyword match, not
// semantic). Both are gated on the KB being indexed (`ready`).
export function KnowledgeBaseBrowserDialog({ kb, onOpenChange }: Props) {
  return (
    <Dialog open={kb !== null} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] sm:max-w-2xl">
        {kb && <BrowserBody kb={kb} />}
      </DialogContent>
    </Dialog>
  )
}

function BrowserBody({ kb }: { kb: KnowledgeBase }) {
  const { t } = useTranslation('knowledge')
  const ready = kb.ingest_status === 'ready'

  return (
    <>
      <DialogHeader>
        <DialogTitle className="flex items-center gap-2">
          <FileText className="size-4" />
          {t('browser.title')} · {kb.name}
        </DialogTitle>
      </DialogHeader>

      {!ready ? (
        <p className="text-sm text-warm-text-muted py-6 text-center">
          {t('browser.notReady')}
        </p>
      ) : (
        <Tabs defaultValue="documents" className="flex flex-col gap-3">
          <TabsList>
            <TabsTrigger value="documents">
              {t('browser.tabDocuments')}
            </TabsTrigger>
            <TabsTrigger value="search">{t('browser.tabSearch')}</TabsTrigger>
          </TabsList>
          <TabsContent value="documents">
            <DocumentsTab kbId={kb.id} />
          </TabsContent>
          <TabsContent value="search">
            <SearchTab kbId={kb.id} />
          </TabsContent>
        </Tabs>
      )}
    </>
  )
}

function DocumentsTab({ kbId }: { kbId: number }) {
  const { t } = useTranslation('knowledge')
  const { data, isLoading, isError } = useQuery({
    queryKey: ['kb-documents', kbId],
    queryFn: () => listKBDocuments(kbId),
  })

  if (isLoading) {
    return (
      <p className="text-sm text-warm-text-muted py-4">
        {t('browser.documentsLoading')}
      </p>
    )
  }
  if (isError) {
    return (
      <p className="flex items-center gap-2 text-sm text-destructive py-4">
        <AlertCircle className="size-4" aria-hidden />
        {t('browser.loadError')}
      </p>
    )
  }
  if (!data || data.length === 0) {
    return (
      <p className="text-sm text-warm-text-muted py-4">
        {t('browser.documentsEmpty')}
      </p>
    )
  }

  return (
    <ul className="flex flex-col divide-y divide-warm-border/60 max-h-[50vh] overflow-y-auto">
      {data.map((doc) => (
        <li key={doc.id} className="flex items-center gap-3 py-2">
          <FileText
            className="size-4 text-warm-text-muted shrink-0"
            aria-hidden
          />
          <div className="flex-1 min-w-0">
            <p className="text-sm truncate">{doc.title || doc.path}</p>
            <p className="text-xs text-warm-text-muted truncate">{doc.path}</p>
          </div>
          <span className="shrink-0 text-xs text-warm-text-muted">
            {t('browser.docChunks', { count: doc.chunk_count })}
          </span>
        </li>
      ))}
    </ul>
  )
}

function SearchTab({ kbId }: { kbId: number }) {
  const { t } = useTranslation('knowledge')
  const [input, setInput] = useState('')
  const [submitted, setSubmitted] = useState('')

  const { data, isFetching, isError } = useQuery({
    queryKey: ['kb-search', kbId, submitted],
    queryFn: () => searchKnowledgeBase(kbId, submitted),
    enabled: submitted.trim().length > 0,
  })

  const submit = () => setSubmitted(input.trim())

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center gap-2">
        <Input
          value={input}
          placeholder={t('browser.searchPlaceholder')}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') submit()
          }}
        />
        <Button
          size="sm"
          variant="outline"
          disabled={input.trim().length === 0 || isFetching}
          onClick={submit}
        >
          <Search className="size-4 mr-1" />
          {isFetching ? t('browser.searching') : t('browser.search')}
        </Button>
      </div>
      <p className="text-xs text-warm-text-muted">
        {t('browser.searchKeywordNote')}
      </p>

      {isError && (
        <p className="flex items-center gap-2 text-sm text-destructive">
          <AlertCircle className="size-4" aria-hidden />
          {t('browser.loadError')}
        </p>
      )}

      {!isError && submitted.trim().length === 0 && (
        <p className="text-sm text-warm-text-muted py-2">
          {t('browser.resultsHint')}
        </p>
      )}

      {!isError &&
        submitted.trim().length > 0 &&
        !isFetching &&
        (!data || data.length === 0) && (
          <p className="text-sm text-warm-text-muted py-2">
            {t('browser.resultsEmpty')}
          </p>
        )}

      {data && data.length > 0 && (
        <ul className="flex flex-col gap-2 max-h-[45vh] overflow-y-auto">
          {data.map((hit, i) => (
            <li
              key={`${hit.document_id}:${hit.chunk_index}:${i}`}
              className="rounded border border-warm-border/60 p-2"
            >
              <p className="text-xs text-warm-text-muted truncate">
                {t('browser.hitLocation', {
                  path: hit.document_path,
                  index: hit.chunk_index,
                })}
              </p>
              <p className="text-sm mt-1 whitespace-pre-wrap break-words">
                {hit.snippet}
              </p>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
