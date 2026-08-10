import { describe, it, expect } from 'vitest'

import { injectDatasetSource } from './echarts-renderer'
import type { ResultSet } from '@/types/data'

const result: ResultSet = {
  columns: [
    { name: 'day', type: 'string' },
    { name: 'cnt', type: 'number' },
  ],
  rows: [
    ['mon', 3],
    ['tue', 5],
  ],
  truncated: false,
  duration_ms: 1,
  engine: 'mysql',
}

describe('injectDatasetSource', () => {
  it('fills source + derives dimensions for an object dataset, keeping other keys', () => {
    const option = { dataset: { sourceHeader: false }, series: [{ type: 'bar', encode: { x: 'day', y: 'cnt' } }] }
    const out = injectDatasetSource(option, result) as { dataset: Record<string, unknown> }
    expect(out.dataset.source).toEqual(result.rows)
    expect(out.dataset.dimensions).toEqual(['day', 'cnt'])
    expect(out.dataset.sourceHeader).toBe(false) // preserved
  })

  it('injects into the FIRST dataset and preserves later transform datasets', () => {
    const option = {
      dataset: [
        {},
        { transform: { type: 'sort', config: { dimension: 'cnt', order: 'desc' } } },
      ],
      series: [{ type: 'bar', datasetIndex: 1, encode: { x: 'day', y: 'cnt' } }],
    }
    const out = injectDatasetSource(option, result) as { dataset: Array<Record<string, unknown>> }
    expect(out.dataset[0].source).toEqual(result.rows)
    expect(out.dataset[0].dimensions).toEqual(['day', 'cnt'])
    // The transform dataset is untouched so the agent's sort chain still runs.
    expect(out.dataset[1].transform).toEqual({
      type: 'sort',
      config: { dimension: 'cnt', order: 'desc' },
    })
  })

  it('keeps agent-declared dimensions (which may carry types/order)', () => {
    const option = {
      dataset: { dimensions: [{ name: 'day', type: 'ordinal' }, { name: 'cnt', type: 'int' }] },
    }
    const out = injectDatasetSource(option, result) as { dataset: Record<string, unknown> }
    expect(out.dataset.dimensions).toEqual([
      { name: 'day', type: 'ordinal' },
      { name: 'cnt', type: 'int' },
    ])
    expect(out.dataset.source).toEqual(result.rows)
  })

  it('returns the option unchanged when it declares no dataset', () => {
    const option = { series: [{ type: 'bar', data: [1, 2, 3] }] }
    expect(injectDatasetSource(option, result)).toBe(option)
  })
})
