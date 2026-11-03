import type { ComponentType, ReactNode } from 'react'
import type { TFunction } from 'i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Inbox, Zap, AlertTriangle } from 'lucide-react'
import { Switch } from '@/components/ui/switch'
import { Button } from '@/components/ui/button'
import {
  missionControlApi,
  type IntakeDailyCount,
  type MissionControlMetric,
  type MissionControlView,
} from '@/lib/mission-control-api'
import { cn } from '@/lib/utils'

// Mission control (design §6.7.8): the Intake and Completion panels are
// independent cards — never merged into a single autonomy score. Each tile is a
// link that drills into the underlying page; a tile with no data renders "—"
// (a nil value from the API), never a noisy 0.

function formatCompact(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`
  return String(Math.round(n))
}

function formatMetric(metric: MissionControlMetric, t: TFunction): string {
  if (metric.value === null || metric.status === 'no_data') return t('missionControl.noData')
  const v = metric.value
  switch (metric.unit) {
    case 'percent':
      return `${new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 }).format(v)}%`
    case 'minutes':
      return t('missionControl.unitMinute', { value: new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 }).format(v) })
    case 'rounds':
      return t('missionControl.unitRound', { value: new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 }).format(v) })
    case 'tokens':
      return formatCompact(v)
    case 'usd':
      return `$${v.toFixed(2)}`
    default:
      return new Intl.NumberFormat().format(Math.round(v))
  }
}

function MetricContent({
  metric,
  label,
  big = false,
}: {
  metric: MissionControlMetric
  label: string
  big?: boolean
}) {
  const { t } = useTranslation('intake')
  const noData = metric.value === null || metric.status === 'no_data'
  const warn = metric.status === 'warning'
  return (
    <>
      <div className="text-xs text-muted-foreground">{label}</div>
      <div
        className={cn(
          'font-semibold tabular-nums',
          big ? 'text-2xl' : 'text-lg',
          noData ? 'text-muted-foreground' : warn ? 'text-warning' : 'text-foreground',
        )}
      >
        {formatMetric(metric, t)}
      </div>
      {/* Abnormality is stated in words as well as colour (design §6.7.8). */}
      {warn ? (
        <div className="flex items-center gap-1 text-xs text-warning">
          <AlertTriangle className="h-3 w-3" aria-hidden="true" />
          <span>{t('missionControl.warning')}</span>
        </div>
      ) : metric.denom && metric.denom > 0 && metric.num !== undefined ? (
        <div className="text-xs text-muted-foreground tabular-nums">
          {metric.num}/{metric.denom}
        </div>
      ) : null}
    </>
  )
}

const TILE_CLASS =
  'rounded-md border bg-background p-3 flex flex-col gap-1 text-left hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring'

function MetricTile({ metric }: { metric: MissionControlMetric }) {
  const { t } = useTranslation('intake')
  const label = t(`missionControl.metrics.${metric.key}`)
  const content = <MetricContent metric={metric} label={label} big />
  switch (metric.drill) {
    case 'intake':
      return (
        <Link to="/intake" className={TILE_CLASS} aria-label={label}>
          {content}
        </Link>
      )
    case 'workspaces':
      return (
        <Link to="/workspaces" className={TILE_CLASS} aria-label={label}>
          {content}
        </Link>
      )
    default:
      // Cost tiles drill into the token trend already rendered on this page.
      return (
        <a href="#owner-token-trend" className={TILE_CLASS} aria-label={label}>
          {content}
        </a>
      )
  }
}

function Panel({
  title,
  subtitle,
  icon: Icon,
  headerRight,
  children,
}: {
  title: string
  subtitle: string
  icon: ComponentType<{ className?: string }>
  headerRight?: ReactNode
  children: ReactNode
}) {
  return (
    <section className="rounded-lg border bg-card p-4 space-y-3" aria-label={title}>
      <div className="flex items-center gap-2">
        <Icon className="w-4 h-4 text-muted-foreground" aria-hidden="true" />
        <span className="text-sm font-medium">{title}</span>
        {headerRight ? <div className="ml-auto">{headerRight}</div> : null}
      </div>
      <p className="text-xs text-muted-foreground">{subtitle}</p>
      {children}
    </section>
  )
}

function DailyList({
  title,
  rows,
  renderKey,
}: {
  title: string
  rows: IntakeDailyCount[] | null | undefined
  renderKey: (row: IntakeDailyCount) => string
}) {
  if (!rows || rows.length === 0) return null
  return (
    <div className="space-y-1">
      <div className="text-xs font-medium text-muted-foreground">{title}</div>
      <ul className="divide-y divide-border rounded-md border">
        {rows.slice(0, 5).map((row, i) => (
          <li key={`${renderKey(row)}-${i}`}>
            <Link
              to="/intake"
              search={{ source: row.source_kind }}
              className="flex items-center justify-between gap-2 px-2 py-1.5 text-xs hover:bg-accent"
            >
              <span className="truncate text-muted-foreground">
                {renderKey(row)} · {row.day}
              </span>
              <span className="tabular-nums">{row.count}</span>
            </Link>
          </li>
        ))}
      </ul>
    </div>
  )
}

export function MissionControlPanels({ ownerParam }: { ownerParam?: string | null }) {
  const { t } = useTranslation('intake')
  const queryClient = useQueryClient()

  const { data, isLoading, isError, refetch } = useQuery<MissionControlView>({
    queryKey: ['mission-control', ownerParam ?? null],
    queryFn: () => missionControlApi.get(ownerParam),
    refetchInterval: 60_000,
    staleTime: 30_000,
    retry: 1,
  })

  const pauseMutation = useMutation({
    mutationFn: (paused: boolean) => missionControlApi.setPaused(paused, ownerParam),
    onSuccess: (res) => {
      queryClient.invalidateQueries({ queryKey: ['mission-control'] })
      toast.success(res.paused ? t('intake.pausePausedToast') : t('intake.pauseResumedToast'))
    },
    onError: (err: unknown) => {
      toast.error(
        t('intake.pauseFailed', { message: err instanceof Error ? err.message : String(err) }),
      )
    },
  })

  if (isLoading && !data) {
    return (
      <div className="grid gap-4 md:grid-cols-2" aria-busy="true">
        <div className="h-56 rounded-lg border bg-card" />
        <div className="h-56 rounded-lg border bg-card" />
      </div>
    )
  }

  if (isError && !data) {
    return (
      <div className="rounded-lg border bg-card p-4 flex items-center justify-between gap-3">
        <span className="text-sm text-muted-foreground">{t('missionControl.loadError')}</span>
        <Button variant="outline" size="sm" onClick={() => refetch()}>
          {t('missionControl.retry')}
        </Button>
      </div>
    )
  }

  const view = data
  if (!view) return null

  const intakeRows = view.intake.metrics
  const completionRows = view.completion.metrics

  return (
    <div className="grid gap-4 md:grid-cols-2">
      <Panel
        title={t('missionControl.intakeTitle')}
        subtitle={t('missionControl.intakeSubtitle')}
        icon={Inbox}
        headerRight={
          <label className="flex items-center gap-2 text-xs text-muted-foreground">
            <span>{t('missionControl.pauseSwitch')}</span>
            <Switch
              checked={view.intake.paused}
              onCheckedChange={(next) => pauseMutation.mutate(next)}
              disabled={pauseMutation.isPending || view.intake.project_count === 0}
              aria-label={t('missionControl.pauseSwitch')}
            />
          </label>
        }
      >
        <div className="grid grid-cols-2 gap-3">
          {intakeRows.map((m) => (
            <MetricTile key={m.key} metric={m} />
          ))}
        </div>
        <DailyList
          title={t('missionControl.perSource')}
          rows={view.intake.per_source}
          renderKey={(row) => row.source_kind}
        />
        <DailyList
          title={t('missionControl.perChat')}
          rows={view.intake.per_chat}
          renderKey={(row) => row.thread_ext_id || row.source_kind}
        />
      </Panel>

      <Panel
        title={t('missionControl.completionTitle')}
        subtitle={t('missionControl.completionSubtitle')}
        icon={Zap}
      >
        <div className="grid grid-cols-2 gap-3">
          {completionRows.map((m) => (
            <MetricTile key={m.key} metric={m} />
          ))}
        </div>
      </Panel>
    </div>
  )
}
