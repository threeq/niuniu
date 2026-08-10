import { lazy, Suspense } from 'react'
import { useTranslation } from 'react-i18next'
import { Copy, Pin } from 'lucide-react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import type { DataBlock } from '@/types/data'

// Lazy-load the echarts renderer so the charting library lands in its own
// async chunk and is only fetched when a non-table chart is actually rendered.
const EChartsRenderer = lazy(() => import('./echarts-renderer'))

export interface DataResultBlockProps {
  data: DataBlock
  /**
   * Invoked when the user clicks "Pin to dashboard". The actual dashboard API
   * wiring happens in a later integration task; this component only surfaces
   * the button and forwards the click.
   */
  onPin?: (data: DataBlock) => void
  /**
   * Invoked when the user clicks "Pin live data" on an echarts block that came
   * from an integrated data source. Unlike onPin (which snapshots the current
   * inline echarts option), this asks the agent to rebuild the chart as a live,
   * data-source-bound panel. Only surfaced when onPinDynamic is provided AND the
   * block is an echarts chart carrying a `source` (see showDynamicPin below).
   */
  onPinDynamic?: (data: DataBlock) => void
  /**
   * Body-only mode: render just the table/chart, with no card border, title,
   * or toolbar (Pin/Copy). Used by the dashboard, which supplies its own panel
   * chrome (title, badge, return-to-workspace, delete) around the chart.
   */
  bare?: boolean
  /** Tailwind height class for the chart container (default h-64). */
  heightClass?: string
}

export function DataResultBlock({
  data,
  onPin,
  onPinDynamic,
  bare,
  heightClass = 'h-64',
}: DataResultBlockProps) {
  const { t } = useTranslation('data')
  const { title, result, chart } = data

  // Every chart here is echarts-rendered except the plain `table`. When a chart
  // came from an integrated data source (it carries `source`), offer "Pin live
  // data" alongside the static snapshot: it asks the agent to (re)build the
  // chart as a re-runnable, source-bound panel via pin_query. This covers the
  // data-driven specs (line/bar/area/pie/scatter) and the self-contained
  // `echarts` option (whose inline data must be rebuilt to be data-driven).
  const showDynamicPin =
    !!onPinDynamic && chart.type !== 'table' && data.source != null

  const handleCopy = () => {
    if (!result) return
    const header = result.columns.map((c) => c.name).join('\t')
    const body = result.rows
      .map((row) => row.map((cell) => String(cell ?? '')).join('\t'))
      .join('\n')
    const text = `${header}\n${body}`
    void navigator.clipboard?.writeText(text).then(
      () => toast.success(t('copied')),
      () => {
        /* clipboard unavailable; swallow */
      },
    )
  }

  const body =
    chart.type === 'table' ? (
      <>
        <Table>
          <TableHeader>
            <TableRow>
              {(result?.columns ?? []).map((col) => (
                <TableHead key={col.name}>{col.name}</TableHead>
              ))}
            </TableRow>
          </TableHeader>
          <TableBody>
            {(result?.rows ?? []).map((row, ri) => (
              <TableRow key={ri}>
                {row.map((cell, ci) => (
                  <TableCell key={ci}>{String(cell ?? '')}</TableCell>
                ))}
              </TableRow>
            ))}
          </TableBody>
        </Table>
        {result?.truncated && (
          <p className="mt-2 text-xs text-muted-foreground">
            {t('truncatedNote')}
          </p>
        )}
      </>
    ) : (
      <Suspense
        fallback={
          <div
            className={`flex ${heightClass} w-full items-center justify-center text-sm text-muted-foreground`}
          >
            {t('chartLoading')}
          </div>
        }
      >
        <EChartsRenderer result={result} chart={chart} heightClass={heightClass} />
      </Suspense>
    )

  // Body-only: the dashboard renders its own card + header around this.
  if (bare) {
    return <div className="w-full">{body}</div>
  }

  return (
    <div className="rounded-md border border-border bg-card">
      <div className="flex items-center justify-between gap-2 border-b border-border px-3 py-2">
        <span className="truncate text-sm font-medium text-foreground">
          {title}
        </span>
        <div className="flex items-center gap-1">
          {showDynamicPin && (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => onPinDynamic?.(data)}
            >
              <Pin className="mr-1 h-4 w-4" />
              {t('pinDynamic')}
            </Button>
          )}
          <Button variant="ghost" size="sm" onClick={() => onPin?.(data)}>
            <Pin className="mr-1 h-4 w-4" />
            {showDynamicPin ? t('pinStatic') : t('pinToDashboard')}
          </Button>
          {/* "Save query" is hidden for M1 — the standalone saved-query store
              has no UI yet, so the button would be a no-op. Pin (which also
              saves a read-only query) + Copy cover the M1 surface. Copy is
              hidden for self-contained echarts blocks that carry no tabular
              result. */}
          {result && (
            <Button variant="ghost" size="sm" onClick={handleCopy}>
              <Copy className="mr-1 h-4 w-4" />
              {t('copy')}
            </Button>
          )}
        </div>
      </div>

      <div className="p-3">{body}</div>
    </div>
  )
}
