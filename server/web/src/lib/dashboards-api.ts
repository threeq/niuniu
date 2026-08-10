// REST wrappers for the data-dashboard feature (saved queries / dashboards /
// panels / pin). Backend handlers in server/internal/api/dashboard.go; routes
// wired under /api in server/internal/server/router.go.
//
// Spec: docs/superpowers/specs/2026-06-04-data-integration-and-dashboard-design.md §7
// Plan task E5.
import { api } from '@/lib/api'
import type { ChartSpec, ResultSet } from '@/types/data'

/** A data dashboard (card on the list page). */
export interface Dashboard {
  id: number
  name: string
  created_at?: string
  updated_at?: string
}

/**
 * One panel on a dashboard. `workspace_id` is the origin workspace the query
 * was pinned from (nullable) and drives the click-to-workspace navigation on
 * the detail page.
 */
export interface DashboardPanel {
  id: number
  dashboard_id: number
  title: string
  viz_type: string
  chart_spec: ChartSpec
  /**
   * Backing data source id. 0 means a static / direct-result panel (fixed
   * inline chart, no re-runnable query); > 0 means a live query panel that
   * re-fetches from the source on each load.
   */
  source_id: number
  workspace_id: number | null
  grid_x: number
  grid_y: number
  grid_w: number
  grid_h: number
  /**
   * Auto-refresh cadence in seconds. 0 means no auto-refresh (fetch once on
   * load). A live panel re-queries on this interval so its "data time"
   * (result.queried_at) reflects the latest successful fetch.
   */
  refresh_interval_sec: number
}

/**
 * Body for POST /api/dashboards/pin (one-step pin from chat).
 *
 * Two shapes:
 *  - re-runnable query: provide `source` + `operation.statement`.
 *  - static / direct-result chart: omit `source`/`operation`; the chart renders
 *    from `chart_spec` and an optional inline `snapshot.result`.
 */
export interface PinQueryBody {
  source?: string | number
  name: string
  operation?: { statement: string }
  chart_spec: ChartSpec
  /**
   * Static snapshot payload. `queried_at` (RFC3339) records when the data was
   * obtained — for a chat-pinned snapshot this is the source message's creation
   * time — and surfaces as the panel's "data time".
   */
  snapshot?: { result?: ResultSet; queried_at?: string }
  workspace_id?: number
  dashboard_id?: number
}

export interface PinQueryResult {
  dashboard_id: number
  panel_id: number
}

export async function listDashboards(): Promise<Dashboard[]> {
  // Backend wraps list responses as { items: [...] } (see DashboardHandler);
  // api.get returns the raw body, so unwrap here.
  const r = await api.get<{ items: Dashboard[] }>('/dashboards')
  return r.items ?? []
}

export async function createDashboard(name: string): Promise<{ id: number }> {
  // Backend returns { id } on create (201), not the full Dashboard.
  return api.post<{ id: number }>('/dashboards', { name })
}

export async function deleteDashboard(id: number): Promise<void> {
  await api.delete<void>(`/dashboards/${id}`)
}

export async function listPanels(dashboardId: number): Promise<DashboardPanel[]> {
  // Backend wraps list responses as { items: [...] }; unwrap (see listDashboards).
  const r = await api.get<{ items: DashboardPanel[] }>(
    `/dashboards/${dashboardId}/panels`,
  )
  return r.items ?? []
}

export async function deletePanel(
  dashboardId: number,
  panelId: number,
): Promise<void> {
  await api.delete<void>(`/dashboards/${dashboardId}/panels/${panelId}`)
}

export async function fetchPanelData(
  dashboardId: number,
  panelId: number,
): Promise<ResultSet> {
  return api.get<ResultSet>(`/dashboards/${dashboardId}/panels/${panelId}/data`)
}

export async function pinQuery(body: PinQueryBody): Promise<PinQueryResult> {
  return api.post<PinQueryResult>('/dashboards/pin', body)
}

/** Move a panel to another dashboard (removed from the source dashboard). */
export async function movePanel(
  dashboardId: number,
  panelId: number,
  targetDashboardId: number,
): Promise<void> {
  await api.post<void>(`/dashboards/${dashboardId}/panels/${panelId}/move`, {
    target_dashboard_id: targetDashboardId,
  })
}

/** Copy a panel onto another dashboard (kept on the source too). */
export async function copyPanel(
  dashboardId: number,
  panelId: number,
  targetDashboardId: number,
): Promise<{ id: number }> {
  return api.post<{ id: number }>(
    `/dashboards/${dashboardId}/panels/${panelId}/copy`,
    { target_dashboard_id: targetDashboardId },
  )
}

/** A panel pinned from a workspace, with the name of the dashboard it lives on. */
export interface WorkspacePanel extends DashboardPanel {
  dashboard_name: string
}

/** Panels pinned from a given workspace (in-workspace data view). */
export async function listWorkspacePanels(
  workspaceId: number,
): Promise<WorkspacePanel[]> {
  const r = await api.get<{ items: WorkspacePanel[] }>(
    `/workspaces/${workspaceId}/panels`,
  )
  return r.items ?? []
}
