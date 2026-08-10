import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import {
  ArrowRight, ShieldCheck, MessageCircleQuestion, Flag, Hand, CircleDollarSign,
} from 'lucide-react'
import { epicApi } from '@/lib/api'
import type { ExecTimelineEntry, ExecTimelineResponse } from '@/types/api'
import { cn } from '@/lib/utils'

interface Props {
  issueId: number
}

const KIND_ICON: Record<ExecTimelineEntry['kind'], React.ComponentType<{ className?: string }>> = {
  advance:      ArrowRight,
  gate:         ShieldCheck,
  ask_user:     MessageCircleQuestion,
  terminal:     Flag,
  intervention: Hand,
  cost:         CircleDollarSign,
}

// Per-issue execution timeline (spec section 23.7): advance moves, gate results,
// ask_user round-trips, terminal transitions, interventions, and cost — answering
// "why this path / where stuck / how much" that the card projection cannot.
export function ExecutionTimeline({ issueId }: Props) {
  const { t } = useTranslation('projects')
  const { data } = useQuery<ExecTimelineResponse>({
    queryKey: ['exec-timeline', issueId],
    queryFn: () => epicApi.execTimeline(issueId),
    staleTime: 15_000,
  })

  const entries = data?.entries ?? []
  const formatTime = (s: string) => {
    const d = new Date(s)
    return Number.isNaN(d.getTime()) ? s : d.toLocaleString()
  }

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between">
        <h4 className="text-xs font-medium text-warm-text-muted">{t('kanban.execTimeline.title')}</h4>
        {data && data.total_cost > 0 && (
          <span className="text-[10px] tabular-nums text-warm-text-muted">
            {t('kanban.execTimeline.totalCost', { cost: data.total_cost.toFixed(2) })}
          </span>
        )}
      </div>

      {entries.length === 0 ? (
        <p className="text-xs text-warm-text-muted">{t('kanban.execTimeline.empty')}</p>
      ) : (
        <ol className="flex flex-col gap-1.5">
          {entries.map((e) => {
            const Icon = KIND_ICON[e.kind] ?? Flag
            return (
              <li key={e.id} className="flex items-start gap-2">
                <span
                  className={cn(
                    'mt-0.5 flex h-5 w-5 flex-shrink-0 items-center justify-center rounded-full border',
                    e.kind === 'terminal'
                      ? 'border-destructive/30 bg-destructive/10 text-destructive'
                      : e.kind === 'intervention'
                        ? 'border-info/30 bg-info/10 text-info'
                        : 'border-warm-border bg-warm-muted text-warm-text-muted',
                  )}
                >
                  <Icon className="h-3 w-3" aria-hidden="true" />
                </span>
                <div className="min-w-0 flex-1">
                  <p className="text-xs text-warm-text break-words">{e.summary}</p>
                  <p className="text-[10px] tabular-nums text-warm-text-muted">
                    {t(`kanban.execTimeline.kind.${e.kind}`)}
                    {e.cost_usd > 0 && ` · $${e.cost_usd.toFixed(2)}`}
                    {' · '}
                    {formatTime(e.created_at)}
                  </p>
                </div>
              </li>
            )
          })}
        </ol>
      )}
    </div>
  )
}
