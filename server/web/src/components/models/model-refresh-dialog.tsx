import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { ApiError } from '@/lib/api'
import { importModels, refreshPreview } from '@/lib/model-api'
import type { ModelCandidate } from '@/types/model'

function errorDetail(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

/**
 * Previews the model catalogue a provider exposes and imports the checked
 * names into the shared library. Discovery is read-only: nothing is saved
 * until the user confirms the selection.
 */
export function ModelRefreshDialog({
  open,
  onOpenChange,
  providerId,
  providerName,
  onImported,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  providerId: number | null
  providerName: string
  onImported?: () => void
}) {
  const { t } = useTranslation('settings')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [candidates, setCandidates] = useState<ModelCandidate[]>([])
  const [selected, setSelected] = useState<string[]>([])
  const [importing, setImporting] = useState(false)

  useEffect(() => {
    if (!open || providerId == null) return
    let cancelled = false
    setLoading(true)
    setError(null)
    setCandidates([])
    setSelected([])
    refreshPreview(providerId)
      .then((list) => {
        if (cancelled) return
        setCandidates(list)
        setSelected(
          list.filter((c) => !c.already_in_library).map((c) => c.name)
        )
      })
      .catch((err) => {
        if (cancelled) return
        setError(
          err instanceof ApiError && err.code === 'MODEL_REFRESH_UNSUPPORTED'
            ? t('models.refreshUnsupported')
            : t('models.refreshFailed', { detail: errorDetail(err) })
        )
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [open, providerId, t])

  const toggle = (name: string) =>
    setSelected((prev) =>
      prev.includes(name) ? prev.filter((n) => n !== name) : [...prev, name]
    )

  const handleImport = async () => {
    setImporting(true)
    try {
      const { added, skipped } = await importModels(selected)
      toast.success(t('models.imported', { added, skipped }))
      onImported?.()
      onOpenChange(false)
    } catch (err) {
      toast.error(t('models.importFailed', { detail: errorDetail(err) }))
    } finally {
      setImporting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('models.refreshTitle')}</DialogTitle>
          <DialogDescription>
            {t('models.refreshDescription', { name: providerName })}
          </DialogDescription>
        </DialogHeader>

        {loading ? (
          <p className="text-sm text-muted-foreground">{t('models.refreshLoading')}</p>
        ) : error ? (
          <p className="text-sm text-destructive" role="alert">
            {error}
          </p>
        ) : candidates.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t('models.refreshEmpty')}</p>
        ) : (
          <div className="max-h-72 space-y-1 overflow-y-auto">
            {candidates.map((candidate, index) => {
              const inputId = `model-refresh-${index}`
              return (
                <div key={candidate.name} className="flex items-center gap-2">
                  <Checkbox
                    id={inputId}
                    checked={
                      candidate.already_in_library ||
                      selected.includes(candidate.name)
                    }
                    disabled={candidate.already_in_library}
                    onCheckedChange={() => toggle(candidate.name)}
                  />
                  <label
                    htmlFor={inputId}
                    className="flex-1 truncate font-mono text-sm text-foreground"
                  >
                    {candidate.name}
                  </label>
                  {candidate.already_in_library ? (
                    <span className="shrink-0 text-xs text-muted-foreground">
                      {t('models.alreadyInLibrary')}
                    </span>
                  ) : null}
                </div>
              )
            })}
          </div>
        )}

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('common:actions.cancel')}
          </Button>
          <Button
            onClick={handleImport}
            disabled={
              importing || loading || error !== null || selected.length === 0
            }
          >
            {importing
              ? t('common:actions.saving')
              : t('models.importButton', { selected: selected.length })}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
