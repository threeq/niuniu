// Pure option-building logic for the ECharts renderer. Kept in its own module
// (no JSX, no component) so `echarts-renderer.tsx` only exports components and
// the option builders stay unit-testable. `echarts` is imported as a TYPE only,
// so the bundler still splits the library into the renderer's async chunk.
import type * as echarts from 'echarts'

import type { ChartSpec, ResultSet } from '@/types/data'

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
 * Only applied when the option declares a `dataset` (a static inline-data option
 * has none and is rendered untouched).
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
export function buildOption(
  resultArg: ResultSet | undefined,
  chart: ChartSpec,
): echarts.EChartsCoreOption {
  // Escape hatch: the agent supplied a complete native ECharts option. The
  // design-system theme (palette, text/axis/legend/tooltip colors) is applied
  // at init time, so the option is handed straight to setOption and only
  // overrides what it sets explicitly. Declarative JSON only (no eval).
  //
  // HYBRID specs: a data-driven type ('bar'/'line'/…) may ALSO carry a native
  // `option` — the natural way agents emit a self-contained chart while
  // keeping the chart-family type. When that option carries its own `series`
  // it is self-sufficient and wins outright; when it carries no series (a
  // style-only override) the result-derived spec below still runs and the
  // option is spread over it, so styling overrides never swallow data.
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
  const hybridOption = chart.option as Record<string, unknown> | undefined
  if (hybridOption != null && Array.isArray(hybridOption.series) && hybridOption.series.length > 0) {
    return normalizeNativeOption({ ...hybridOption })
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
    // A style-only hybrid `option` (no series — see the hybrid branch above)
    // overrides the derived spec here, alongside the legacy allow-listed
    // `options`.
    ...(chart.option ?? {}),
    ...(chart.options ?? {}),
  }
}
