// Ops Bot console types (design 2026-11-06 §14.3).
//
// The list view reads the mission-control aggregate (owner → channel →
// account); the drilldown reads the per-bot /api/ops routes. Keeping both in
// one file means the list and the detail can never disagree about a status or
// budget shape.

export type OpsBotStatus = 'active' | 'paused' | 'disabled'
export type OpsBotRole = 'account' | 'director'
export type OpsRiskLevel = 'normal' | 'warning' | 'critical'

export interface OpsOutput {
  today: number
  week: number
}

export interface OpsMetricsSnapshot {
  date: string
  values: Record<string, unknown>
  source: string
}

export interface OpsBudgetSnapshot {
  limit: number
  currency: string
  spent: number
  remaining: number
  unlimited: boolean
  configured: boolean
  period_from: string
  period_to: string
}

export interface OpsBrowserSnapshot {
  backend: string
  login_status: string
  health: string
  risk_state: string
  last_error?: string
}

/** Browser login lifecycle reported by GET /api/ops/bots/:id/browser. */
export type OpsLoginState = 'idle' | 'running' | 'succeeded' | 'failed' | 'not_required'

export interface OpsBrowserStatus {
  bot_id: number
  state: OpsLoginState
  login_status: string
  backend: string
  has_session: boolean
  has_login_recipe: boolean
  profile_dir?: string
  last_login_at?: string
  last_error?: string
  started_at?: string
  finished_at?: string
  evidence_path?: string
  next_action: 'none' | 'login' | 'relogin'
  evidence_url?: string
}

export interface OpsAccount {
  bot_id: number
  role: OpsBotRole
  pack_id: string
  pack_version: number
  account_ref: string
  display_name: string
  status: OpsBotStatus
  operator: string
  director_bot_id?: number
  epic_issue_id?: number
  output: OpsOutput
  metrics?: OpsMetricsSnapshot
  budget: OpsBudgetSnapshot
  pending_approvals: number
  high_risk_engagements: number
  risk: OpsRiskLevel
  risk_reasons: string[]
  browser?: OpsBrowserSnapshot
  error?: string
  updated_at: string
}

export interface OpsChannel {
  pack_id: string
  pack_version: number
  accounts: OpsAccount[]
}

export interface OpsOwner {
  owner_type: string
  owner_id: number
  owner_label: string
  channels: OpsChannel[]
  error?: string
}

export interface MissionControlOpsView {
  owners: OpsOwner[]
  account_count: number
}

// --- /api/ops DTOs ---------------------------------------------------------

export interface OpsBot {
  id: number
  owner_type: string
  owner_id: number
  role: OpsBotRole
  pack_id: string
  pack_version: number
  account_ref: string
  display_name: string
  persona: Record<string, unknown>
  credential_refs: Record<string, unknown>
  knowledge_scope: Record<string, unknown>
  goals: Record<string, unknown>
  cadence: Record<string, unknown>
  approval_policy: Record<string, unknown>
  budget: Record<string, unknown>
  epic_issue_id: number | null
  control_workspace_id: number | null
  director_bot_id: number | null
  status: OpsBotStatus
  created_at: string
  updated_at: string
}

export interface OpsBudgetUsage {
  bot_id: number
  period_from: string
  period_to: string
  limit: number
  currency: string
  unlimited: boolean
  spent: number
  remaining: number
  items?: Array<{ category: string; unit: string; quantity: number }>
}

export interface OpsMetricsItem {
  date: string
  metrics: Record<string, unknown>
  source: string
}

export interface OpsCalendarItem {
  issue_id: number
  title: string
  issue_type: string
  lifecycle_status: string
  due_date?: string
  start_date?: string
}

export interface OpsCalendar {
  bot_id: number
  cadence: Record<string, unknown>
  items: OpsCalendarItem[]
  count: number
}

export interface OpsEngagement {
  id: number
  bot_id: number
  issue_id: number | null
  platform_engagement_id: string
  kind: string
  author: string
  text: string
  category: string
  risk_level: string
  suggested_reply: string
  status: string
  receipt: string
  created_at: string
  updated_at: string
}

export interface OpsApproval {
  id: number
  workspace_id: number
  bot_id: number
  type: string
  status: string
  metadata: Record<string, unknown>
  answers: Record<string, unknown>
  decision_source?: string
  decided_by?: number
  requested_at: string
  decided_at?: string
  expires_at: string
}
