// Shared TS types for the data-integration feature.
//
// These mirror the backend JSON shapes (snake_case) defined in
// docs/superpowers/specs/2026-06-04-data-integration-and-dashboard-design.md
// §4.1 (ResultSet / Column) and §6.2 (ChartSpec). The same shapes are reused by
// chat rendering, chart rendering, and the dashboard.

/** Supported data source engines. Mirrors dataconn.SourceKind on the backend. */
export type DataSourceKind =
  | 'mysql' | 'postgres' | 'clickhouse' | 'mssql'
  | 'mariadb' | 'tidb' | 'oceanbase' | 'starrocks' | 'doris'
  | 'cockroachdb' | 'greenplum' | 'redshift' | 'opengauss' | 'polardbpg' | 'yugabyte'
  | 'redis' | 'mongo' | 'elasticsearch' | 'trino' | 'http'

/** A single result column. `type` is normalized: string|number|bool|time|json. */
export interface Column {
  name: string
  type: string
}

/** Normalized query result, shared across chat / chart / dashboard. */
export interface ResultSet {
  columns: Column[]
  rows: unknown[][]
  /** Present for write ops; omitted for reads. */
  rows_affected?: number
  /** True when the row limit was hit and rows were dropped. */
  truncated: boolean
  duration_ms: number
  /** Source kind that produced the result, e.g. "mysql". */
  engine: string
  /**
   * When the data was obtained from the source (RFC3339), stamped by the data
   * proxy. Drives the dashboard panel's "data time": a live panel shows its
   * latest re-query time; a static snapshot carries the time its data was
   * captured. Omitted for results that predate this field.
   */
  queried_at?: string
}

/** How to visualize a ResultSet. Defaults to `table`. */
export interface ChartSpec {
  /**
   * `table` and the chart primitives (line/bar/area/pie/scatter) are derived
   * from the ResultSet via x/y column mapping. `echarts` is the escape hatch:
   * the agent supplies a complete native ECharts `option` and the renderer
   * feeds it straight to `setOption` — full chart flexibility, no ResultSet
   * required.
   */
  type: 'table' | 'line' | 'bar' | 'area' | 'pie' | 'scatter' | 'echarts'
  /** Dimension column name (x axis). */
  x?: string
  /** Measure column names (y series). */
  y?: string[]
  /** Optional grouping column. */
  series?: string
  /** Limited allow-listed options passed through to the chart library. */
  options?: Record<string, unknown>
  /**
   * Full native ECharts option, used when `type === 'echarts'`. Plain JSON
   * (declarative config only — no functions / no eval); handed directly to
   * `echarts.setOption`. Design-system colors are applied as a default and can
   * be overridden here.
   */
  option?: Record<string, unknown>
}

/** A fenced `niuniu-data` block emitted by the agent into a chat message. */
export interface DataBlock {
  title?: string
  /**
   * Tabular result. Optional: a `chart.type === 'echarts'` block can render a
   * self-contained chart with no underlying ResultSet.
   */
  result?: ResultSet
  chart: ChartSpec
  /**
   * Data source name or id the query ran against. Carried so the chat "Pin to
   * dashboard" button can persist a re-runnable saved query. Optional because a
   * block may be a one-off display without a pinnable origin.
   */
  source?: string | number
  /** The single read statement that produced `result` (SQL/Trino sources). */
  statement?: string
  /**
   * The NoSQL read operation that produced `result` (redis/mongo/elasticsearch),
   * carried so an operation-based chart can also be pinned as a live, re-runnable
   * panel — the SQL `statement`'s counterpart for NoSQL sources.
   */
  operation?: Record<string, unknown>
}
