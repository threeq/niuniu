import { useTranslation } from 'react-i18next'
import { ShieldCheck, RotateCcw, Trash2, Clock, Eraser } from 'lucide-react'
import {
  useSweepRuns,
  useReviewQueue,
  useKeepMemory,
  useConfirmDeleteMemory,
  useClearSweepRuns,
} from '@/lib/hooks/use-memory'
import { SOURCE_BADGE_CLASS } from '@/lib/memory-source-badge'
import type { Memory, MemorySweepRun } from '@/types/api'

interface Props {
  projectId: number
}

// MemoryStalenessSection surfaces the automatic memory-staleness management for a
// project: the review queue (memories flagged as no longer matching the latest
// code, awaiting keep/delete) and the execution log of automatic sweeps.
export function MemoryStalenessSection({ projectId }: Props) {
  const { t } = useTranslation('projects')
  const { data: queue = [] } = useReviewQueue(projectId)
  const { data: runs = [] } = useSweepRuns(projectId)
  const keep = useKeepMemory(projectId)
  const del = useConfirmDeleteMemory(projectId)
  const clearRuns = useClearSweepRuns(projectId)

  const triggerLabel = (tr: MemorySweepRun['trigger']) => t(`tabs.memory.staleness.triggers.${tr}`)
  const fmt = (s: string) => new Date(s).toLocaleString()

  return (
    <div className="border rounded-lg p-4 space-y-4 bg-card">
      <div className="flex items-center gap-2">
        <ShieldCheck className="w-4 h-4 text-info shrink-0" />
        <h4 className="text-sm font-medium text-foreground">{t('tabs.memory.staleness.title')}</h4>
      </div>
      <p className="text-xs text-muted-foreground">{t('tabs.memory.staleness.description')}</p>

      {/* Review queue */}
      <div className="space-y-2">
        <div className="text-xs font-medium text-foreground">
          {t('tabs.memory.staleness.reviewQueue')}
          {queue.length > 0 && (
            <span className="ml-1 text-muted-foreground/70">({queue.length})</span>
          )}
        </div>
        {queue.length === 0 ? (
          <p className="text-xs text-muted-foreground">{t('tabs.memory.staleness.reviewEmpty')}</p>
        ) : (
          <div className="space-y-1.5">
            {queue.map((m: Memory) => (
              <div key={m.id} className="border rounded-md px-3 py-2 text-xs space-y-1">
                <div className="flex items-start justify-between gap-2">
                  <div className="min-w-0 flex-1">
                    <span className="font-medium text-foreground">{m.title}</span>
                    {m.stale_reason && (
                      <p className="text-muted-foreground mt-0.5 whitespace-pre-wrap">{m.stale_reason}</p>
                    )}
                  </div>
                  <div className="flex items-center gap-1 shrink-0">
                    <button
                      onClick={() => keep.mutate(m.id)}
                      disabled={keep.isPending}
                      className="inline-flex items-center gap-0.5 px-1.5 py-0.5 rounded text-info hover:bg-accent disabled:opacity-50"
                    >
                      <RotateCcw className="w-3 h-3" />
                      {t('tabs.memory.staleness.keep')}
                    </button>
                    <button
                      onClick={() => del.mutate(m.id)}
                      disabled={del.isPending}
                      className="inline-flex items-center gap-0.5 px-1.5 py-0.5 rounded text-destructive hover:bg-accent disabled:opacity-50"
                    >
                      <Trash2 className="w-3 h-3" />
                      {t('tabs.memory.staleness.delete')}
                    </button>
                  </div>
                </div>
                <span
                  className={`inline-flex items-center px-1.5 py-0.5 rounded border text-[9px] ${SOURCE_BADGE_CLASS[m.source] || ''}`}
                >
                  {t(`tabs.memory.sources.${m.source}`)}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>

      {/* Execution log (per project) */}
      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <div className="text-xs font-medium text-foreground">{t('tabs.memory.staleness.runs')}</div>
          {runs.length > 0 && (
            <button
              onClick={() => clearRuns.mutate()}
              disabled={clearRuns.isPending}
              className="inline-flex items-center gap-0.5 px-1.5 py-0.5 rounded text-xs text-muted-foreground hover:text-destructive hover:bg-accent disabled:opacity-50"
            >
              <Eraser className="w-3 h-3" />
              {t('tabs.memory.staleness.clearHistory')}
            </button>
          )}
        </div>
        {runs.length === 0 ? (
          <p className="text-xs text-muted-foreground">{t('tabs.memory.staleness.runsEmpty')}</p>
        ) : (
          <div className="border border-border rounded-md divide-y divide-border">
            {runs.map((r: MemorySweepRun) => (
              <div key={r.id} className="flex items-center gap-3 px-3 py-2 text-xs">
                <Clock className="w-3 h-3 text-muted-foreground shrink-0" />
                <span className="text-muted-foreground shrink-0">{fmt(r.created_at)}</span>
                <span className="inline-flex items-center px-1.5 py-0.5 rounded bg-muted text-muted-foreground text-[10px] shrink-0">
                  {triggerLabel(r.trigger)}
                </span>
                <span className="text-muted-foreground truncate">
                  {t('tabs.memory.staleness.runSummary', {
                    scanned: r.scanned,
                    deleted: r.auto_deleted,
                    queued: r.queued,
                  })}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
