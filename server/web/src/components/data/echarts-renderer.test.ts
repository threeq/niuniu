import { describe, expect, it } from 'vitest'

import { buildOption, normalizeNativeOption } from './echarts-renderer'

describe('normalizeNativeOption', () => {
  it('pushes a colliding legend below a title with subtext and opens grid.top', () => {
    const out = normalizeNativeOption({
      title: { text: 'T', subtext: 'S' },
      legend: { data: ['a'], top: 50 },
      grid: { top: 90, left: 10 },
    })
    expect((out.legend as Record<string, unknown>).top).toBe(64)
    expect((out.grid as Record<string, unknown>).top).toBe(92)
    expect((out.grid as Record<string, unknown>).left).toBe(10)
  })

  it('pushes a default-top legend below a subtext-less title', () => {
    const out = normalizeNativeOption({
      title: { text: 'T' },
      legend: {},
    })
    expect((out.legend as Record<string, unknown>).top).toBe(38)
    expect((out.grid as Record<string, unknown>).top).toBe(66)
  })

  it('leaves non-colliding or explicitly placed legends alone', () => {
    const bottom = { title: { text: 'T' }, legend: { bottom: 0 } }
    expect(normalizeNativeOption(bottom)).toBe(bottom)

    const below = { title: { text: 'T', subtext: 'S' }, legend: { top: 70 } }
    expect(normalizeNativeOption(below)).toBe(below)

    const middle = { title: { text: 'T' }, legend: { top: 'middle' } }
    expect(normalizeNativeOption(middle)).toBe(middle)

    const hidden = { title: { text: 'T' }, legend: { show: false, top: 0 } }
    expect(normalizeNativeOption(hidden)).toBe(hidden)
  })

  it('ignores options without title or legend objects', () => {
    const noTitle = { legend: { top: 0 } }
    expect(normalizeNativeOption(noTitle)).toBe(noTitle)

    const noLegend = { title: { text: 'T' } }
    expect(normalizeNativeOption(noLegend)).toBe(noLegend)
  })
})

describe('buildOption — hybrid chart specs (data-driven type + native option)', () => {
  // Agents naturally emit {type:'bar', option:{...complete...}} — a
  // chart-family type with a full native option and NO result. The option is
  // self-sufficient (series carry inline data) and must be honored as-is;
  // before the fix buildOption ignored `option` for non-echarts types and
  // rendered an empty chart off the (missing) result.
  it('honors a self-sufficient native option on a bar-typed spec', () => {
    const chart = {
      type: 'bar' as const,
      x: 'day',
      y: ['新建'],
      option: {
        xAxis: { type: 'category', data: ['9/8', '9/9'] },
        yAxis: { type: 'value' },
        series: [{ name: '新建', type: 'bar', data: [13, 21] }],
      },
    }
    const opt = buildOption(undefined, chart)
    expect(opt.series).toEqual(chart.option.series)
    expect(opt.xAxis).toEqual(chart.option.xAxis)
  })

  // A style-only option (no series) on a data-driven type must NOT swallow
  // the result-derived series — the option acts as an override layer.
  it('keeps result-derived series when the option carries no series', () => {
    const chart = {
      type: 'bar' as const,
      x: 'day',
      y: ['count'],
      option: { legend: { bottom: 0 } },
    }
    const result = {
      columns: [{ name: 'day', type: 'string' }, { name: 'count', type: 'number' }],
      rows: [['9/8', 13]],
      truncated: false,
      duration_ms: 1,
      engine: 'mysql',
    }
    const opt = buildOption(result, chart)
    expect(opt.series).toEqual([{ name: 'count', type: 'bar', data: [13] }])
    expect(opt.legend).toEqual({ bottom: 0 })
  })
})
