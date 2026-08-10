import { render, screen } from '@testing-library/react'
import { describe, it, expect } from 'vitest'

import { DataResultBlock } from './data-result-block'
import type { DataBlock, ResultSet } from '@/types/data'

const tableResult: ResultSet = {
  columns: [{ name: 'id', type: 'number' }],
  rows: [[1]],
  truncated: false,
  duration_ms: 1,
  engine: 'mysql',
}

const tableBlock: DataBlock = {
  result: tableResult,
  chart: { type: 'table' },
}

describe('DataResultBlock', () => {
  it('renders a table with the column header and cell value', () => {
    render(<DataResultBlock data={tableBlock} />)
    expect(screen.getByText('id')).toBeInTheDocument()
    expect(screen.getByText('1')).toBeInTheDocument()
  })

  it('shows the truncated note when the result is truncated', () => {
    render(
      <DataResultBlock
        data={{ ...tableBlock, result: { ...tableResult, truncated: true } }}
      />,
    )
    // zh-CN truncated note value from data.json
    expect(screen.getByText(/截断/)).toBeInTheDocument()
  })

  it('renders a self-contained echarts block (no result): title shown, Copy hidden', () => {
    const echartsBlock: DataBlock = {
      title: 'Custom chart',
      chart: { type: 'echarts', option: { series: [{ type: 'bar', data: [1, 2, 3] }] } },
    }
    render(<DataResultBlock data={echartsBlock} />)
    expect(screen.getByText('Custom chart')).toBeInTheDocument()
    // Copy depends on a tabular result, which an echarts-only block lacks.
    expect(screen.queryByText(/Copy|复制/i)).not.toBeInTheDocument()
  })

  it('offers "Pin live data" for an echarts block from an integrated data source', () => {
    const echartsFromSource: DataBlock = {
      title: 'Sales',
      source: 'analytics',
      statement: 'SELECT day, total FROM sales',
      chart: { type: 'echarts', option: { series: [{ type: 'bar', data: [1] }] } },
    }
    render(
      <DataResultBlock
        data={echartsFromSource}
        onPin={() => {}}
        onPinDynamic={() => {}}
      />,
    )
    // Both the static snapshot and the live-data pin entries are shown.
    expect(screen.getByText(/实时数据|live data/i)).toBeInTheDocument()
    expect(screen.getByText(/快照数据|snapshot/i)).toBeInTheDocument()
  })

  it('offers "Pin live data" for a non-echarts chart (e.g. bar) from a data source', () => {
    // Every chart type except `table` is echarts-rendered, so the live-data
    // entry applies to data-driven specs too, not just chart.type==='echarts'.
    const barFromSource: DataBlock = {
      title: 'Sales',
      source: 'analytics',
      statement: 'SELECT day, total FROM sales',
      result: {
        columns: [{ name: 'day', type: 'string' }],
        rows: [['mon']],
        truncated: false,
        duration_ms: 1,
        engine: 'mysql',
      },
      chart: { type: 'bar', x: 'day', y: ['total'] },
    }
    render(<DataResultBlock data={barFromSource} onPin={() => {}} onPinDynamic={() => {}} />)
    expect(screen.getByText(/实时数据|live data/i)).toBeInTheDocument()
  })

  it('does not offer "Pin live data" for a table even with a data source', () => {
    const tableFromSource: DataBlock = {
      source: 'analytics',
      statement: 'SELECT id FROM t',
      result: {
        columns: [{ name: 'id', type: 'number' }],
        rows: [[1]],
        truncated: false,
        duration_ms: 1,
        engine: 'mysql',
      },
      chart: { type: 'table' },
    }
    render(<DataResultBlock data={tableFromSource} onPin={() => {}} onPinDynamic={() => {}} />)
    expect(screen.queryByText(/实时数据|live data/i)).not.toBeInTheDocument()
  })

  it('does not offer "Pin live data" for an echarts block without a data source', () => {
    const echartsNoSource: DataBlock = {
      title: 'Custom chart',
      chart: { type: 'echarts', option: { series: [{ type: 'bar', data: [1] }] } },
    }
    render(<DataResultBlock data={echartsNoSource} onPin={() => {}} onPinDynamic={() => {}} />)
    expect(screen.queryByText(/实时数据|live data/i)).not.toBeInTheDocument()
  })

  it('hides the dead "Save query" button for M1 (Pin + Copy only)', () => {
    render(<DataResultBlock data={tableBlock} />)
    // saveQuery i18n value must not render; Pin + Copy remain.
    expect(screen.queryByText(/另存|保存查询|Save query/i)).not.toBeInTheDocument()
    expect(screen.getByText(/Pin|固定|钉/i)).toBeInTheDocument()
  })
})
