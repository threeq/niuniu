import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { FileText, AlertCircle, Sparkles } from 'lucide-react'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { readKBDocument, type KBDocument } from '@/lib/kb-api'

interface Props {
  kbId: number
  doc: KBDocument | null
  onOpenChange: (open: boolean) => void
}

/**
 * Reads one KB document's text in-app.
 *
 * This closes the loop the old UI left open: you could see that a document
 * existed, and that a search matched inside it, but there was no way to read it —
 * the only recourse was to leave niuniu and open the file from disk. PDF/Office
 * documents are extracted server-side, so they are readable here too.
 */
export function KBDocumentReader({ kbId, doc, onOpenChange }: Props) {
  return (
    <Dialog open={doc !== null} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] sm:max-w-3xl">
        {doc && <ReaderBody kbId={kbId} doc={doc} />}
      </DialogContent>
    </Dialog>
  )
}

function ReaderBody({ kbId, doc }: { kbId: number; doc: KBDocument }) {
  const { t } = useTranslation('knowledge')
  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['kb-document-content', kbId, doc.id],
    queryFn: () => readKBDocument(kbId, doc.id),
  })

  return (
    <>
      <DialogHeader>
        <DialogTitle className="flex items-center gap-2 min-w-0">
          <FileText className="size-4 shrink-0" aria-hidden />
          <span className="truncate">{doc.title || doc.path}</span>
        </DialogTitle>
      </DialogHeader>

      <p className="text-xs text-muted-foreground font-mono break-all -mt-2">
        {doc.path}
      </p>

      {isLoading && (
        <p className="text-sm text-muted-foreground py-6 text-center">
          {t('common:actions.loading')}
        </p>
      )}

      {isError && (
        <p className="flex items-start gap-2 text-sm text-destructive py-4">
          <AlertCircle className="size-4 shrink-0 mt-0.5" aria-hidden />
          {error instanceof Error ? error.message : t('reader.loadError')}
        </p>
      )}

      {data && (
        <>
          <div className="flex items-center gap-2 flex-wrap">
            {data.extracted && (
              <span className="inline-flex items-center gap-1 rounded border border-info/30 bg-info/15 px-1.5 py-0.5 text-xs text-info">
                <Sparkles className="size-3" aria-hidden />
                {t('reader.extracted')}
              </span>
            )}
            {data.truncated && (
              <span className="inline-flex items-center gap-1 rounded border border-warning/30 bg-warning/15 px-1.5 py-0.5 text-xs text-warning">
                <AlertCircle className="size-3" aria-hidden />
                {t('reader.truncated')}
              </span>
            )}
          </div>
          <pre className="flex-1 min-h-0 max-h-[60vh] overflow-auto rounded-md border border-warm-border bg-warm-muted p-3 text-xs font-mono whitespace-pre-wrap break-words">
            {data.content}
          </pre>
        </>
      )}
    </>
  )
}
