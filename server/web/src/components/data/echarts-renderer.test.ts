import { describe, expect, it } from 'vitest'

import { normalizeNativeOption } from './echarts-renderer'

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
