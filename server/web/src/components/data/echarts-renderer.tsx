import { Component, useEffect, useRef, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import * as echarts from 'echarts'

import { useThemeStore } from '@/stores/theme-store'
import type { ChartSpec, ResultSet } from '@/types/data'

// IMPORTANT: this is the ONLY module that imports `echarts`. Keeping the import
// isolated here lets the bundler split echarts into its own async chunk, which
// `<DataResultBlock>` loads lazily via `React.lazy(() => import(...))`.

/**
 * Read a design-system color token off the document root and wrap it as an
 * `hsl()` string. The tokens in index.css are stored as bare HSL component
 * tuples (e.g. `218 75% 46%`), so they must be wrapped before use. Reading at
 * render time means the values track light/dark automatically (the `.dark`
 * class on <html> swaps the underlying tuples). No hardcoded hex.
 *
 * zrender's color parser strips spaces then splits on commas, so the modern
 * space-separated syntax `hsl(218 75% 46%)` parses to `undefined` and the
 * color silently falls back — the tuple must be joined with commas.
 */
function token(name: string, fallback: string): string {
  if (typeof document === 'undefined') return hslTuple(fallback)
  const raw = getComputedStyle(document.documentElement)
    .getPropertyValue(name)
    .trim()
  if (!raw) return hslTuple(fallback)
  // Already a full color value (hsl(...)/rgb(...)/#...) -> use as-is.
  if (/^(#|rgb|hsl)/i.test(raw)) return raw
  return hslTuple(raw)
}

/** `218 75% 46%` -> `hsl(218, 75%, 46%)`; `h s% l% / a` -> `hsla(h, s%, l%, a)`. */
function hslTuple(tuple: string): string {
  const [color, alpha] = tuple.split('/').map((s) => s.trim())
  const parts = color.split(/\s+/).join(', ')
  return alpha ? `hsla(${parts}, ${alpha})` : `hsl(${parts})`
}

/** Ordered palette pulled from the design system, used for chart series. */
function palette(): string[] {
  return [
    token('--brand', '218 75% 46%'),
    token('--info', '221 83% 53%'),
    token('--success', '142 71% 45%'),
    token('--warning', '38 92% 50%'),
    token('--destructive', '0 84% 60%'),
  ]
}

/**
 * Build an ECharts theme from the design-system tokens. Passed to
 * `echarts.init` so component-level defaults (title/legend/axis/tooltip —
 * which have their own light-background default colors that are unreadable in
 * dark mode) are themed for the current light/dark palette. The agent's option
 * merges on top, so anything it sets explicitly still wins.
 */
function buildTheme(): object {
  const text = token('--foreground', '0 0% 10%')
  const muted = token('--muted-foreground', '0 0% 45%')
  const line = token('--border', '0 0% 90%')
  const axis = {
    axisLine: { lineStyle: { color: line } },
    axisTick: { lineStyle: { color: line } },
    axisLabel: { color: muted },
    nameTextStyle: { color: muted },
    splitLine: { lineStyle: { color: line } },
  }
  return {
    color: palette(),
    textStyle: { color: text },
    title: {
      textStyle: { color: text, fontWeight: 600 },
      subtextStyle: { color: muted },
    },
    legend: { textStyle: { color: muted } },
    tooltip: {
      backgroundColor: token('--popover', '0 0% 100%'),
      borderColor: line,
      textStyle: { color: token('--popover-foreground', '0 0% 10%') },
    },
    categoryAxis: axis,
    valueAxis: axis,
    timeAxis: axis,
    logAxis: axis,
  }
}

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

/**
 * Inject a live ResultSet into an echarts option's `dataset` source — the
 * eval-free path for a dynamic (live) data-source panel whose result shape
 * varies. The agent authors a data-driven option: a `dataset` (with optional
 * echarts BUILT-IN transforms — filter / sort / etc. — and `series.encode` to
 * map dimensions), leaving the raw rows out. On each re-query the dashboard
 * fills the FIRST (raw) dataset's `source` from the result rows and `dimensions`
 * from the query columns (agent-declared dimensions are kept if present, so they
 * can carry types/order). Datasets carrying a `transform` are preserved, so the
 * agent's declarative filter/sort/aggregate chain runs on the fresh rows. No
 * functions, no eval — pure echarts dataset wiring.
 *
 * Exported for unit testing. Only applied when the option declares a `dataset`
 * (a static inline-data option has none and is rendered untouched).
 */
export function injectDatasetSource(
  option: Record<string, unknown>,
  result: ResultSet,
): Record<string, unknown> {
  const fill = (d: unknown): Record<string, unknown> => {
    const obj = isPlainObject(d) ? d : {}
    return {
      ...obj,
      source: result.rows,
      dimensions: obj.dimensions ?? result.columns.map((c) => c.name),
    }
  }
  const ds = option.dataset
  if (Array.isArray(ds)) {
    const [first, ...rest] = ds
    return { ...option, dataset: [fill(first), ...rest] }
  }
  if (isPlainObject(ds)) {
    return { ...option, dataset: fill(ds) }
  }
  return option
}

/**
 * Agent-authored native options routinely stack title + subtext + legend at
 * the top using the ECharts defaults, which draws the legend on top of the
 * subtitle (both components default to the same top region and ECharts does
 * no collision layout). When the legend would land inside the estimated title
 * block, push it below — and open up grid.top to match so it does not land on
 * the plot instead. Explicit non-colliding placements are left untouched.
 */
export function normalizeNativeOption(
  option: Record<string, unknown>,
): Record<string, unknown> {
  const title = option.title
  const legend = option.legend
  if (!isPlainObject(title) || !isPlainObject(legend)) return option
  if (!title.text || legend.show === false) return option
  // Already pinned elsewhere vertically (bottom / middle / percentage).
  if (legend.bottom != null) return option
  if (typeof legend.top === 'string' && legend.top !== 'top') return option
  // Estimated title block height: default title fontSize 18 (~26px line) plus,
  // with a subtext, itemGap 10 + subtext fontSize 12 (~20px line) and padding.
  const titleTop = typeof title.top === 'number' ? title.top : 0
  const blockBottom = titleTop + (title.subtext ? 64 : 38)
  const legendTop = typeof legend.top === 'number' ? legend.top : 0
  if (legendTop >= blockBottom) return option
  const next: Record<string, unknown> = {
    ...option,
    legend: { ...legend, top: blockBottom },
  }
  const grid = isPlainObject(option.grid) ? option.grid : {}
  const gridTop = typeof grid.top === 'number' ? grid.top : 60 // echarts default
  const legendBottom = blockBottom + 28
  if (gridTop < legendBottom) next.grid = { ...grid, top: legendBottom }
  return next
}

/** Index of a column name within the result set, or -1 if absent. */
function columnIndex(result: ResultSet, name: string | undefined): number {
  if (!name) return -1
  return result.columns.findIndex((c) => c.name === name)
}

const EMPTY_RESULT: ResultSet = {
  columns: [],
  rows: [],
  truncated: false,
  duration_ms: 0,
  engine: '',
}

/** Map a ResultSet + ChartSpec into an ECharts option object. */
function buildOption(
  resultArg: ResultSet | undefined,
  chart: ChartSpec,
): echarts.EChartsCoreOption {
  // Escape hatch: the agent supplied a complete native ECharts option. The
  // design-system theme (palette, text/axis/legend/tooltip colors) is applied
  // at init time, so the option is handed straight to setOption and only
  // overrides what it sets explicitly. Declarative JSON only (no eval).
  if (chart.type === 'echarts') {
    const opt = normalizeNativeOption({ ...(chart.option ?? {}) })
    // Dynamic data-source echarts: the agent declared a `dataset` (+ optional
    // built-in transforms + series.encode); inject the live re-queried rows into
    // its source. Static inline options have no dataset and pass through.
    if (resultArg && resultArg.columns.length > 0 && opt.dataset != null) {
      return injectDatasetSource(opt, resultArg)
    }
    return opt
  }

  const result = resultArg ?? EMPTY_RESULT
  const xIdx = columnIndex(result, chart.x)
  const yNames = chart.y ?? []

  // Pie: take the first y column as the value, x as the category label.
  if (chart.type === 'pie') {
    const valueIdx = columnIndex(result, yNames[0])
    const data = result.rows.map((row, i) => ({
      name: xIdx >= 0 ? String(row[xIdx]) : String(i),
      value: valueIdx >= 0 ? Number(row[valueIdx]) : 0,
    }))
    return {
      tooltip: { trigger: 'item' },
      legend: {},
      series: [
        {
          type: 'pie',
          radius: ['40%', '70%'],
          data,
        },
      ],
      ...(chart.options ?? {}),
    }
  }

  // Scatter: x and the first y as numeric coordinates.
  if (chart.type === 'scatter') {
    const yIdx = columnIndex(result, yNames[0])
    const data = result.rows.map((row) => [
      xIdx >= 0 ? Number(row[xIdx]) : 0,
      yIdx >= 0 ? Number(row[yIdx]) : 0,
    ])
    return {
      tooltip: { trigger: 'item' },
      xAxis: { type: 'value' },
      yAxis: { type: 'value' },
      series: [{ type: 'scatter', data }],
      ...(chart.options ?? {}),
    }
  }

  // line / bar / area: shared category-axis layout, one series per y column.
  const categories = result.rows.map((row, i) =>
    xIdx >= 0 ? String(row[xIdx]) : String(i),
  )
  const isArea = chart.type === 'area'
  const echartsType = chart.type === 'bar' ? 'bar' : 'line'

  const series = yNames.map((name) => {
    const idx = columnIndex(result, name)
    return {
      name,
      type: echartsType,
      ...(isArea ? { areaStyle: {} } : {}),
      data: result.rows.map((row) =>
        idx >= 0 ? Number(row[idx]) : null,
      ),
    }
  })

  return {
    tooltip: { trigger: 'axis' },
    legend: {},
    grid: { left: '3%', right: '4%', bottom: '3%', containLabel: true },
    xAxis: { type: 'category', data: categories },
    yAxis: { type: 'value' },
    series,
    ...(chart.options ?? {}),
  }
}

export interface EChartsRendererProps {
  /** Optional: omitted when `chart.type === 'echarts'` (self-contained option). */
  result?: ResultSet
  chart: ChartSpec
  /** Tailwind height class for the chart container (default h-64). */
  heightClass?: string
}

// failureNotice builds the inline "chart failed to render" node imperatively, so
// the effect can swap it into the container WITHOUT setState (which the
// render-hooks lint rule forbids inside an effect) — keeping a bad option
// isolated to this one chart.
function failureNotice(message: string): HTMLDivElement {
  const div = document.createElement('div')
  div.className =
    'flex h-full w-full items-center justify-center rounded border border-border bg-muted/40 p-4 text-center text-xs text-muted-foreground'
  div.textContent = message
  return div
}

function EChartsRendererInner({
  result,
  chart,
  heightClass = 'h-64',
}: EChartsRendererProps) {
  const { t } = useTranslation('data')
  const containerRef = useRef<HTMLDivElement>(null)
  const failMessage = t('chartRenderFailed')
  // Theme colors are captured at init time (echarts has no "re-theme" API), so
  // a light/dark toggle must dispose + re-init — otherwise e.g. a chart drawn
  // in dark mode keeps its near-white title on the now-white background.
  const resolvedTheme = useThemeStore((s) => s.resolvedTheme)

  useEffect(() => {
    const el = containerRef.current
    if (!el) return
    el.replaceChildren() // clear any prior canvas / failure notice

    let instance: echarts.ECharts | null = null
    try {
      instance = echarts.init(el, buildTheme())
      instance.setOption(buildOption(result, chart))
    } catch (e) {
      // A bad option — e.g. a transform/encode over a dimension that isn't in
      // the result — must fail THIS chart only. Catching here keeps the throw
      // from escaping the effect and unmounting the whole dashboard / chat; the
      // notice is written imperatively so a later re-query can recover.
      console.error('echarts render failed', e)
      instance?.dispose()
      el.replaceChildren(failureNotice(failMessage))
      return
    }

    const inst = instance
    const resize = () => inst.resize()
    const observer = new ResizeObserver(resize)
    observer.observe(el)

    return () => {
      observer.disconnect()
      inst.dispose()
    }
  }, [result, chart, resolvedTheme, failMessage])

  return <div ref={containerRef} className={`${heightClass} w-full`} />
}

interface ChartErrorBoundaryProps {
  resetKey: unknown
  fallback: ReactNode
  children: ReactNode
}

interface ChartErrorBoundaryState {
  hasError: boolean
  key: unknown
}

// Isolates a chart crash to its own panel: a render/lifecycle error in one
// chart shows the fallback instead of unmounting the page. Resets when resetKey
// changes (e.g. a live panel re-queries) so a transient bad option recovers on
// the next data. Belt-and-suspenders with the inner try/catch, which already
// handles the common setOption throw.
class ChartErrorBoundary extends Component<
  ChartErrorBoundaryProps,
  ChartErrorBoundaryState
> {
  state: ChartErrorBoundaryState = { hasError: false, key: this.props.resetKey }

  static getDerivedStateFromError(): Partial<ChartErrorBoundaryState> {
    return { hasError: true }
  }

  static getDerivedStateFromProps(
    props: ChartErrorBoundaryProps,
    state: ChartErrorBoundaryState,
  ): Partial<ChartErrorBoundaryState> | null {
    if (props.resetKey !== state.key) {
      return { hasError: false, key: props.resetKey }
    }
    return null
  }

  componentDidCatch(err: unknown) {
    console.error('chart render crashed', err)
  }

  render() {
    return this.state.hasError ? this.props.fallback : this.props.children
  }
}

export default function EChartsRenderer(props: EChartsRendererProps) {
  return (
    <ChartErrorBoundary
      resetKey={props.result ?? props.chart}
      fallback={<ChartFailureNotice heightClass={props.heightClass} />}
    >
      <EChartsRendererInner {...props} />
    </ChartErrorBoundary>
  )
}

function ChartFailureNotice({ heightClass = 'h-64' }: { heightClass?: string }) {
  const { t } = useTranslation('data')
  return (
    <div
      className={`flex ${heightClass} w-full items-center justify-center rounded border border-border bg-muted/40 p-4 text-center text-xs text-muted-foreground`}
    >
      {t('chartRenderFailed')}
    </div>
  )
}
