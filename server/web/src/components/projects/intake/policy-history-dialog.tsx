import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { AlertTriangle } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { LoadingSkeleton } from '@/components/shared/loading-skeleton'
import { intakeApi, type IntakeSourceKind } from '@/lib/intake-api'
import { cn } from '@/lib/utils'
import { diffSnapshots } from './intake-utils'

interface Props {
  projectId: number
  sourceKind: IntakeSourceKind | null
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function PolicyHistoryDialog({ projectId, sourceKind, open, onOpenChange }: Props) {
  const { t, i18n } = useTranslation('intake')
  const [selectedId, setSelectedId] = useState<number | null>(null)

  const { data: versions = [], isLoading, isError, refetch } = useQuery({
    queryKey: ['intake-policy-versions', projectId, sourceKind],
    queryFn: () => intakeApi.listPolicyVersions(projectId, sourceKind as IntakeSourceKind),
    enabled: open && sourceKind !== null,
    retry: false,
  })

  // The parent remounts per source (key), so the selection starts at newest.
  const selectedIndex = selectedId === null
    ? 0
    : Math.max(0, versions.findIndex((v) => v.id === selectedId))
  const selected = versions[selectedIndex]
  // versions are newest first, so the "before" side is the next row down.
  const previous = versions[selectedIndex + 1]
  const rows = selected ? diffSnapshots(previous?.snapshot, selected.snapshot) : []

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t('intake.history.title')}</DialogTitle>
          <DialogDescription>{t('intake.history.description')}</DialogDescription>
        </DialogHeader>

        {isLoading ? (
          <LoadingSkeleton variant="card" count={2} />
        ) : isError ? (
          <Alert variant="destructive">
            <AlertTriangle className="h-4 w-4" aria-hidden="true" />
            <AlertTitle>{t('intake.history.loadError')}</AlertTitle>
            <AlertDescription>
              <Button type="button" variant="outline" size="sm" onClick={() => refetch()}>
                {t('intake.retry')}
              </Button>
            </AlertDescription>
          </Alert>
        ) : versions.length === 0 ? (
          <p className="py-6 text-center text-sm text-muted-foreground">{t('intake.history.empty')}</p>
        ) : (
          <div className="grid gap-4 sm:grid-cols-4">
            <div className="max-h-72 overflow-y-auto rounded-md border border-warm-border sm:col-span-1">
              {versions.map((v) => (
                <Button
                  key={v.id}
                  type="button"
                  variant="ghost"
                  onClick={() => setSelectedId(v.id)}
                  aria-current={v.id === selected?.id ? 'true' : undefined}
                  className={cn(
                    'flex h-auto w-full flex-col items-start justify-start gap-0.5 rounded-none border-b border-warm-border px-3 py-2 text-left last:border-b-0',
                    v.id === selected?.id ? 'bg-brand-soft text-brand' : 'hover:bg-warm-muted',
                  )}
                >
                  <span className="text-sm font-medium tabular-nums">
                    {t('intake.history.version', { version: v.version })}
                  </span>
                  <span className="text-xs text-muted-foreground">
                    {new Date(v.created_at).toLocaleString(i18n.language)}
                  </span>
                </Button>
              ))}
            </div>

            <div className="min-w-0 space-y-2 sm:col-span-3">
              <h4 className="text-sm font-medium">
                {selected ? t('intake.history.version', { version: selected.version }) : null}
              </h4>
              <div className="space-y-1.5">
                {rows.map((row) => (
                  <div
                    key={row.field}
                    className={cn(
                      'rounded-md border px-2.5 py-2 text-xs',
                      row.changed
                        ? 'border-warm-border bg-warm-muted/60'
                        : 'border-transparent opacity-60',
                    )}
                  >
                    <div className="font-medium">{t(`intake.history.field.${row.field}`)}</div>
                    {row.changed ? (
                      <p className="mt-1 break-words text-muted-foreground">
                        <span className="line-through">{row.before}</span>
                        <span className="px-1" aria-hidden="true">→</span>
                        <span className="text-foreground">{row.after}</span>
                      </p>
                    ) : (
                      <p className="mt-1 break-words text-muted-foreground">{row.after}</p>
                    )}
                  </div>
                ))}
              </div>
            </div>
          </div>
        )}

        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
            {t('intake.history.close')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
