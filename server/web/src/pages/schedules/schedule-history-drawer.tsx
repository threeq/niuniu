import { useState, useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { RefreshCw, Pin } from 'lucide-react'
import { Sheet, SheetContent, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { api } from '@/lib/api'
import { cn } from '@/lib/utils'
import type { GlobalSchedule, ScheduleRun } from '@/types/api'

interface ScheduleHistoryDrawerProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  schedule: GlobalSchedule | null
}

export function ScheduleHistoryDrawer({ open, onOpenChange, schedule }: ScheduleHistoryDrawerProps) {
  const { t } = useTranslation('schedules')
  const [beforeId, setBeforeId] = useState<number | null>(null)
  const [allRuns, setAllRuns] = useState<ScheduleRun[]>([])

  const statusConfig = useMemo(
    () => ({
      triggered: { label: t('history.statusTriggered'), bg: 'bg-success/10 dark:bg-emerald-950/30', text: 'text-success dark:text-emerald-300', dot: 'bg-success' },
      skipped: { label: t('history.statusSkipped'), bg: 'bg-warning/10 dark:bg-amber-950/30', text: 'text-warning dark:text-amber-300', dot: 'bg-warning' },
      failed: { label: t('history.statusFailed'), bg: 'bg-destructive/10 dark:bg-red-950/30', text: 'text-destructive dark:text-red-300', dot: 'bg-destructive' },
    }),
    [t],
  )

  const { data: runs = [], isFetching } = useQuery({
    queryKey: ['schedule-runs', schedule?.id, beforeId],
    queryFn: async () => {
      const params = new URLSearchParams({ limit: '20' })
      if (beforeId) params.set('before', String(beforeId))
      const result = await api.get<ScheduleRun[]>(
        `/schedules/${schedule!.id}/runs?${params.toString()}`
      )
      if (beforeId) {
        setAllRuns((prev) => [...prev, ...result])
      } else {
        setAllRuns(result)
      }
      return result
    },
    enabled: open && !!schedule,
  })

  const handleOpenChange = (isOpen: boolean) => {
    if (!isOpen) {
      setBeforeId(null)
      setAllRuns([])
    }
    onOpenChange(isOpen)
  }

  const loadMore = () => {
    if (allRuns.length > 0) {
      setBeforeId(allRuns[allRuns.length - 1].id)
    }
  }

  const hasMore = runs.length === 20

  return (
    <Sheet open={open} onOpenChange={handleOpenChange}>
      <SheetContent side="right" className="w-[400px] sm:w-[450px] overflow-y-auto">
        <SheetHeader>
          <SheetTitle className="text-base">
            {schedule?.name || (schedule?.schedule_type === 'cron' ? schedule.cron_expr : t('history.onceTitle'))}
          </SheetTitle>
          {schedule && (
            <div className="flex items-center gap-2 text-xs text-muted-foreground">
              {schedule.schedule_type === 'cron' ? (
                <><RefreshCw className="w-3 h-3" /><span className="font-mono">{schedule.cron_expr}</span></>
              ) : (
                <><Pin className="w-3 h-3" /><span>{schedule.run_at ? new Date(schedule.run_at).toLocaleString() : t('history.onceLabel')}</span></>
              )}
              <span className={cn(
                'px-1.5 py-0.5 rounded text-[10px]',
                schedule.enabled ? 'bg-success/10 text-success dark:bg-emerald-950/30 dark:text-emerald-300' : 'bg-muted text-muted-foreground'
              )}>
                {schedule.enabled ? t('history.enabled') : t('history.disabled')}
              </span>
            </div>
          )}
        </SheetHeader>

        <div className="mt-6">
          <h4 className="text-sm font-medium text-foreground mb-3">{t('history.heading')}</h4>

          {allRuns.length === 0 && !isFetching ? (
            <p className="text-xs text-muted-foreground italic">{t('history.empty')}</p>
          ) : (
            <div className="space-y-1">
              {allRuns.map((run) => {
                const config = statusConfig[run.status as keyof typeof statusConfig]
                if (!config) return null
                return (
                  <div
                    key={run.id}
                    className="flex items-center gap-3 py-2 px-3 rounded-md hover:bg-accent"
                  >
                    <span className={cn('w-1.5 h-1.5 rounded-full shrink-0', config.dot)} />
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-2">
                        <span className="text-xs text-foreground">
                          {new Date(run.created_at).toLocaleString()}
                        </span>
                        <span className={cn(
                          'px-1.5 py-0.5 rounded text-[10px]',
                          config.bg, config.text
                        )}>
                          {config.label}
                        </span>
                      </div>
                      {run.message && (
                        <p className="text-[11px] text-muted-foreground/70 mt-0.5 truncate">{run.message}</p>
                      )}
                    </div>
                  </div>
                )
              })}
            </div>
          )}

          {hasMore && (
            <button
              onClick={loadMore}
              disabled={isFetching}
              className="w-full mt-3 py-2 text-xs text-info hover:text-info/80 disabled:opacity-50"
            >
              {isFetching ? t('common:actions.loading') : t('history.loadMore')}
            </button>
          )}
        </div>
      </SheetContent>
    </Sheet>
  )
}
