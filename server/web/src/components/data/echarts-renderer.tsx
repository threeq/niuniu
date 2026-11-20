import { Component, useEffect, useRef, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import * as echarts from 'echarts'

import { useThemeStore } from '@/stores/theme-store'
import { buildOption } from './echarts-options'
import type { ChartSpec, ResultSet } from '@/types/data'

// IMPORTANT: this is the ONLY module that imports `echarts` at runtime. Keeping
// the import isolated here lets the bundler split echarts into its own async chunk, which
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
