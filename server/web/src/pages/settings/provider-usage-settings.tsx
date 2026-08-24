import { lazy, Suspense, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { AlertTriangle, Server } from 'lucide-react'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'
import { useThemeStore } from '@/stores/theme-store'
import { EmptyState } from '@/components/shared/empty-state'
import { Badge } from '@/components/ui/badge'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import type {
  ProviderHourBucket,
  ProviderRateLimitEvent,
  ProviderRateLimitEventsResponse,
  ProviderUsageResponse,
  ProviderUsageTotal,
} from '@/types/api'
import type { ChartSpec } from '@/types/data'

// echarts stays in its own async chunk (same escape hatch the workspace token
// chart uses), so this settings tab does not pull it into the main bundle.
const EChartsRenderer = lazy(() => import('@/components/data/echarts-renderer'))

// Windows the user can report over. The backend keeps 90 days of
// platform-grain history, so 90d is the widest option that can return
// complete data — offering more would silently show a truncated series.
const RANGE_DAYS = [1, 7, 30, 90] as const
type RangeDays = (typeof RANGE_DAYS)[number]

// Hourly buckets are the storage grain. Over a long window there are too many
// to read as columns, so the chart rolls them up by day past this threshold —
// the underlying data stays hourly either way.
const HOURLY_CHART_MAX_DAYS = 7

// Series colors come from the chart palette tokens, cycled per platform. The
// count is deliberately small: a user juggling more subscription platforms than
// this gets repeats rather than invented colors outside the design system.
const PLATFORM_COLOR_VARS: { colorVar: string; fallback: string }[] = [
  { colorVar: '--brand', fallback: '218 75% 46%' },
  { colorVar: '--info', fallback: '221 83% 53%' },
  { colorVar: '--warning', fallback: '38 92% 50%' },
  { colorVar: '--success', fallback: '142 71% 45%' },
  { colorVar: '--col-review', fallback: '262 70% 56%' },
  { colorVar: '--col-impl', fallback: '30 82% 41%' },
]

/**
 * Read a design-system color token off the document root, wrapping bare HSL
 * tuples as comma-separated `hsl(...)`. Mirrors token-usage-chart's helper:
 * duplicated rather than shared so building the option never pulls the echarts
 * module into this eagerly-loaded component.
 */
function cssToken(name: string, fallback: string): string {
  if (typeof document === 'undefined') return hslTuple(fallback)
  const raw = getComputedStyle(document.documentElement).getPropertyValue(name).trim()
  if (!raw) return hslTuple(fallback)
  if (/^(#|rgb|hsl)/i.test(raw)) return raw
  return hslTuple(raw)
}

function hslTuple(tuple: string): string {
  const [color, alpha] = tuple.split('/').map((s) => s.trim())
  const parts = color.split(/\s+/).join(', ')
  return alpha ? `hsla(${parts}, ${alpha})` : `hsl(${parts})`
}

function compact(n: number): string {
  const abs = Math.abs(n)
  if (abs >= 1e9) return (n / 1e9).toFixed(1).replace(/\.0$/, '') + 'B'
  if (abs >= 1e6) return (n / 1e6).toFixed(1).replace(/\.0$/, '') + 'M'
  if (abs >= 1e3) return (n / 1e3).toFixed(1).replace(/\.0$/, '') + 'K'
  return String(n)
}

/** `8/24 15:00` for an hourly bucket, `8/24` when rolled up by day. */
function formatBucket(iso: string, daily: boolean): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const md = `${d.getMonth() + 1}/${d.getDate()}`
  return daily ? md : `${md} ${String(d.getHours()).padStart(2, '0')}:00`
}

function formatDateTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString()
}

/**
 * Human-readable duration from seconds, e.g. `2h 15m` / `45m` / `30s`. Kept to
 * two units so the table column stays narrow.
 */
function formatDuration(seconds: number): string {
  if (seconds <= 0) return '-'
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const s = seconds % 60
  if (h > 0) return m > 0 ? `${h}h ${m}m` : `${h}h`
  if (m > 0) return s > 0 ? `${m}m ${s}s` : `${m}m`
  return `${s}s`
}

/**
 * Pivot the flat (hour, provider) rows the API returns into one stacked series
 * per platform over a shared, sorted time axis.
 *
 * `daily` collapses each bucket to its calendar day first. Buckets are keyed by
 * their ISO string so a platform that was idle in a bucket contributes 0 there
 * rather than shifting its remaining points onto the wrong slots.
 */
function pivotByPlatform(buckets: ProviderHourBucket[], daily: boolean) {
  const keyOf = (iso: string) => {
    if (!daily) return iso
    const d = new Date(iso)
    if (Number.isNaN(d.getTime())) return iso
    return new Date(d.getFullYear(), d.getMonth(), d.getDate()).toISOString()
  }

  const axis: string[] = []
  const seenKey = new Set<string>()
  const names = new Map<number, string>()
  // provider id -> bucket key -> summed tokens
  const totals = new Map<number, Map<string, number>>()

  for (const b of buckets) {
    const key = keyOf(b.hour)
    if (!seenKey.has(key)) {
      seenKey.add(key)
      axis.push(key)
    }
    if (!names.has(b.provider_id)) {
      names.set(b.provider_id, b.provider_name)
    }
    let byKey = totals.get(b.provider_id)
    if (!byKey) {
      byKey = new Map<string, number>()
      totals.set(b.provider_id, byKey)
    }
    const sum =
      b.input_tokens + b.output_tokens + b.cache_creation_tokens + b.cache_read_tokens
    byKey.set(key, (byKey.get(key) ?? 0) + sum)
  }

  axis.sort((a, b) => new Date(a).getTime() - new Date(b).getTime())
  const series = [...totals.entries()].map(([providerID, byKey]) => ({
    providerID,
    name: names.get(providerID) ?? String(providerID),
    data: axis.map((key) => byKey.get(key) ?? 0),
  }))
  return { axis, series }
}

/** Stacked column chart: total tokens per bucket, one stack segment per platform. */
function PlatformUsageChart({
  buckets,
  daily,
}: {
  buckets: ProviderHourBucket[]
  daily: boolean
}) {
  const { t } = useTranslation('settings')
  // Re-read the swapped token tuples on light/dark toggle; the new option
  // identity makes EChartsRenderer re-init with them.
  useThemeStore((s) => s.resolvedTheme)

  const { axis, series } = useMemo(() => pivotByPlatform(buckets, daily), [buckets, daily])

  if (series.length === 0) {
    return (
      <div className="flex items-center justify-center py-10 text-sm text-muted-foreground">
        {t('providerUsage.chart.empty')}
      </div>
    )
  }

  const axisColor = cssToken('--muted-foreground', '0 0% 45%')
  const splitColor = cssToken('--border', '0 0% 90%')

  const option: Record<string, unknown> = {
    color: series.map(
      (_, i) =>
        cssToken(
          PLATFORM_COLOR_VARS[i % PLATFORM_COLOR_VARS.length].colorVar,
          PLATFORM_COLOR_VARS[i % PLATFORM_COLOR_VARS.length].fallback
        )
    ),
    grid: { left: 4, right: 8, top: 30, bottom: 2, containLabel: true },
    legend: {
      top: 0,
      left: 0,
      itemWidth: 10,
      itemHeight: 10,
      itemGap: 14,
      icon: 'roundRect',
      textStyle: { color: axisColor, fontSize: 11 },
    },
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'shadow' },
      valueFormatter: (v: number | null) => (v == null ? '-' : compact(Number(v))),
    },
    xAxis: {
      type: 'category',
      data: axis,
      boundaryGap: true,
      axisTick: { show: false },
      axisLine: { lineStyle: { color: splitColor } },
      axisLabel: {
        color: axisColor,
        fontSize: 10,
        hideOverlap: true,
        formatter: (v: string) => formatBucket(v, daily),
      },
    },
    yAxis: {
      type: 'value',
      axisLabel: { color: axisColor, fontSize: 10, formatter: (v: number) => compact(v) },
      splitLine: { lineStyle: { color: splitColor, opacity: 0.4 } },
    },
    series: series.map((s, i) => ({
      name: s.name,
      type: 'bar',
      stack: 'tokens',
      barMaxWidth: 20,
      // Round only the topmost series so the stacked column gets one cap.
      itemStyle: { borderRadius: i === series.length - 1 ? [3, 3, 0, 0] : 0 },
      emphasis: { focus: 'series' },
      data: s.data,
    })),
  }

  const chart: ChartSpec = { type: 'echarts', option }
  return (
    <Suspense fallback={<div className="h-56 w-full animate-pulse rounded bg-muted/40" />}>
      <EChartsRenderer chart={chart} heightClass="h-56" />
    </Suspense>
  )
}

/** Per-platform totals for the window, with each platform's throttle tally. */
function PlatformTotalsTable({ totals }: { totals: ProviderUsageTotal[] }) {
  const { t } = useTranslation('settings')
  if (totals.length === 0) return null

  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>{t('providerUsage.table.platform')}</TableHead>
          <TableHead className="text-right">{t('providerUsage.table.total')}</TableHead>
          <TableHead className="text-right">{t('providerUsage.table.input')}</TableHead>
          <TableHead className="text-right">{t('providerUsage.table.output')}</TableHead>
          <TableHead className="text-right">{t('providerUsage.table.cache')}</TableHead>
          <TableHead className="text-right">{t('providerUsage.table.turns')}</TableHead>
          <TableHead className="text-right">{t('providerUsage.table.limits')}</TableHead>
          <TableHead className="text-right">{t('providerUsage.table.blocked')}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {totals.map((p) => (
          <TableRow key={p.provider_id}>
            <TableCell className="font-medium">
              <div className="flex items-center gap-2">
                <span className="truncate">{p.provider_name}</span>
                {p.open_count > 0 && (
                  <Badge
                    variant="outline"
                    className="gap-1 border-warning/30 bg-warning/15 text-warning"
                  >
                    <AlertTriangle className="h-3 w-3" aria-hidden="true" />
                    {t('providerUsage.table.limitedNow')}
                  </Badge>
                )}
              </div>
            </TableCell>
            <TableCell className="text-right tabular-nums">{compact(p.total_tokens)}</TableCell>
            <TableCell className="text-right tabular-nums text-muted-foreground">
              {compact(p.input_tokens)}
            </TableCell>
            <TableCell className="text-right tabular-nums text-muted-foreground">
              {compact(p.output_tokens)}
            </TableCell>
            <TableCell className="text-right tabular-nums text-muted-foreground">
              {compact(p.cache_creation_tokens + p.cache_read_tokens)}
            </TableCell>
            <TableCell className="text-right tabular-nums text-muted-foreground">
              {p.interaction_count}
            </TableCell>
            <TableCell className="text-right tabular-nums">
              {p.rate_limit_count > 0 ? p.rate_limit_count : '-'}
            </TableCell>
            <TableCell className="text-right tabular-nums text-muted-foreground">
              {formatDuration(p.blocked_seconds)}
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  )
}

/** The 429 episode log: when each platform throttled us and when it resumed. */
function RateLimitLog({ events }: { events: ProviderRateLimitEvent[] }) {
  const { t } = useTranslation('settings')
  if (events.length === 0) {
    return (
      <div className="py-6 text-center text-sm text-muted-foreground">
        {t('providerUsage.limits.empty')}
      </div>
    )
  }

  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>{t('providerUsage.table.platform')}</TableHead>
          <TableHead>{t('providerUsage.limits.triggeredAt')}</TableHead>
          <TableHead>{t('providerUsage.limits.resetAt')}</TableHead>
          <TableHead>{t('providerUsage.limits.resumedAt')}</TableHead>
          <TableHead className="text-right">{t('providerUsage.limits.duration')}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {events.map((e) => (
          <TableRow key={e.id}>
            <TableCell className="font-medium">{e.provider_name}</TableCell>
            <TableCell className="text-muted-foreground">
              {formatDateTime(e.triggered_at)}
            </TableCell>
            <TableCell className="text-muted-foreground">{formatDateTime(e.reset_at)}</TableCell>
            <TableCell>
              {e.resumed_at ? (
                <span className="text-muted-foreground">{formatDateTime(e.resumed_at)}</span>
              ) : (
                <Badge
                  variant="outline"
                  className="gap-1 border-warning/30 bg-warning/15 text-warning"
                >
                  <AlertTriangle className="h-3 w-3" aria-hidden="true" />
                  {t('providerUsage.limits.stillLimited')}
                </Badge>
              )}
              {e.cleared_manually && (
                <span className="ml-2 text-xs text-muted-foreground">
                  {t('providerUsage.limits.clearedManually')}
                </span>
              )}
            </TableCell>
            <TableCell className="text-right tabular-nums">
              {e.resumed_at ? formatDuration(e.blocked_seconds) : '-'}
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  )
}

/**
 * ProviderUsageSettings reports token consumption per subscription platform at
 * hourly grain, plus the rate-limit episode log (429 trigger time, the reset the
 * platform promised, and when the platform was actually used again).
 *
 * Owner-scoped: a platform's quota is consumed across every workspace bound to
 * it, so per-workspace numbers would not answer "how much of this plan is left".
 */
export function ProviderUsageSettings() {
  const { t } = useTranslation('settings')
  const userId = useAuthStore((s) => s.user?.id ?? 0)
  const [rangeDays, setRangeDays] = useState<RangeDays>(7)

  // Personal edition has no auth, so user?.id is 0; the backend resolves
  // "user:0" to the single local owner exactly as the workspace token chart
  // relies on.
  const owner = `user:${userId}`
  // Recomputed per range change rather than per render, so the query key (and
  // therefore the cache entry) is stable while the user reads the page.
  const { from, to } = useMemo(() => {
    const now = new Date()
    return {
      to: now.toISOString(),
      from: new Date(now.getTime() - rangeDays * 24 * 3600 * 1000).toISOString(),
    }
  }, [rangeDays])

  const { data: usage, isLoading } = useQuery({
    queryKey: ['provider-usage', owner, rangeDays],
    queryFn: () =>
      api.get<ProviderUsageResponse>('/provider-usage', { params: { owner, from, to } }),
  })

  const { data: limits } = useQuery({
    queryKey: ['provider-usage', 'rate-limits', owner, rangeDays],
    queryFn: () =>
      api.get<ProviderRateLimitEventsResponse>('/provider-usage/rate-limits', {
        params: { owner, from, to },
      }),
  })

  const buckets = usage?.buckets ?? []
  const totals = useMemo(() => usage?.totals ?? [], [usage?.totals])
  const events = limits?.events ?? []
  const daily = rangeDays > HOURLY_CHART_MAX_DAYS

  const grandTotal = useMemo(
    () => totals.reduce((sum, p) => sum + p.total_tokens, 0),
    [totals]
  )

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="text-lg font-semibold">{t('providerUsage.title')}</h2>
          <p className="mt-1 text-sm text-muted-foreground">{t('providerUsage.description')}</p>
        </div>
        <Select
          value={String(rangeDays)}
          onValueChange={(v) => setRangeDays(Number(v) as RangeDays)}
        >
          <SelectTrigger className="w-36" aria-label={t('providerUsage.rangeLabel')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {RANGE_DAYS.map((d) => (
              <SelectItem key={d} value={String(d)}>
                {t('providerUsage.range', { count: d })}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {isLoading ? (
        <div className="h-56 w-full animate-pulse rounded-lg bg-muted/40" />
      ) : totals.length === 0 && events.length === 0 ? (
        <EmptyState
          icon={<Server className="h-12 w-12 text-muted-foreground" aria-hidden="true" />}
          title={t('providerUsage.emptyTitle')}
          description={t('providerUsage.emptyDescription')}
          size="compact"
        />
      ) : (
        <>
          <div className="rounded-lg border bg-card p-4">
            <div className="mb-3 flex items-baseline justify-between gap-2">
              <div className="text-sm font-medium">
                {daily ? t('providerUsage.chart.titleDaily') : t('providerUsage.chart.title')}
              </div>
              <div className="text-xs text-muted-foreground tabular-nums">
                {t('providerUsage.chart.grandTotal', { value: compact(grandTotal) })}
              </div>
            </div>
            <PlatformUsageChart buckets={buckets} daily={daily} />
          </div>

          <div className="rounded-lg border bg-card p-4">
            <div className="mb-3 text-sm font-medium">{t('providerUsage.table.title')}</div>
            <PlatformTotalsTable totals={totals} />
          </div>

          <div className="rounded-lg border bg-card p-4">
            <div className="mb-1 text-sm font-medium">{t('providerUsage.limits.title')}</div>
            <p className="mb-3 text-xs text-muted-foreground">
              {t('providerUsage.limits.description')}
            </p>
            <RateLimitLog events={events} />
          </div>
        </>
      )}
    </div>
  )
}
