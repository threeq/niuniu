import { api } from '@/lib/api'

// Client for the Intake (任务来源) / Completion (自动完成) endpoints defined in
// docs/superpowers/specs/2026-11-03-intake-completion-autonomy-design.md §6.9.

export type IntakeMode = 'off' | 'observe' | 'auto'
export type IntakeSourceKind = 'im_bot' | 'github' | 'ci' | 'email' | 'webhook'

/** One (project, source) settings row. Kinds without a stored row come back
 *  with schema defaults (observe / cap 4 / empty lists). */
export interface IntakeSetting {
  source_kind: IntakeSourceKind
  mode: IntakeMode
  daily_cap: number
  allow_list: string[]
  bot_list: string[]
  updated_at?: string
}

export interface IntakeSettings {
  project_id: number
  paused: boolean
  settings: IntakeSetting[]
}

export interface IntakeRuleConditions {
  actors?: string[]
  keywords?: string[]
}

export interface IntakeRule {
  id: number
  project_id: number
  source_kind: IntakeSourceKind
  name: string
  instruction: string
  conditions: IntakeRuleConditions
  priority: number
  enabled: boolean
  created_at: string
}

export interface IntakeRuleInput {
  source_kind: IntakeSourceKind
  name: string
  instruction: string
  conditions: IntakeRuleConditions
  priority: number
  enabled?: boolean
}

export interface IntakePolicyVersion {
  id: number
  project_id: number
  source_kind: IntakeSourceKind
  version: number
  snapshot: unknown
  created_at: string
}

/** One dry-run decision, joined with its originating event. */
export interface IntakeDecision {
  id: number
  event_id: number
  project_id: number
  source_kind: IntakeSourceKind
  event_kind: string
  actor_ext_id: string
  actor_name: string
  thread_ext_id: string
  mode_at_decision: IntakeMode
  strength: string
  decision: string
  executed: boolean
  reason_code: string
  reason_text: string
  matched_rule_ids: number[] | null
  policy_version: number
  issue_id: number | null
  workspace_id: number | null
  would_do: unknown
  payload: unknown
  created_at: string
}

export interface CompletionProfile {
  id: number
  name: string
  autohost_enabled: boolean
  max_rounds: number
  token_budget: number
  stop_on_decision: boolean
  require_review: boolean
  issue_count: number
}

export interface CompletionSettings {
  autohost_enabled: boolean
  max_rounds: number
  token_budget: number
  stop_on_decision: boolean
  require_review: boolean
  default_profile_id: number | null
  profiles: CompletionProfile[]
}

export const intakeApi = {
  // suppressError: these settings endpoints are part of the intake surface, so a
  // 404/403 renders the panel's own error state instead of a global toast.
  getSettings: (projectId: number) =>
    api.get<IntakeSettings>(`/projects/${projectId}/intake/settings`, { suppressError: true }),

  updateSettings: (projectId: number, settings: IntakeSettingInput[]) =>
    api.put<IntakeSettings>(`/projects/${projectId}/intake/settings`, { settings }),

  setPaused: (projectId: number, paused: boolean) =>
    api.put<{ paused: boolean }>(`/projects/${projectId}/intake/pause`, { paused }),

  listRules: (projectId: number) =>
    api.get<IntakeRule[]>(`/projects/${projectId}/intake/rules`, { suppressError: true }),

  createRule: (projectId: number, input: IntakeRuleInput) =>
    api.post<IntakeRule>(`/projects/${projectId}/intake/rules`, input),

  updateRule: (projectId: number, ruleId: number, input: IntakeRuleInput) =>
    api.put<IntakeRule>(`/projects/${projectId}/intake/rules/${ruleId}`, input),

  deleteRule: (projectId: number, ruleId: number) =>
    api.delete<void>(`/projects/${projectId}/intake/rules/${ruleId}`),

  listPolicyVersions: (projectId: number, sourceKind: IntakeSourceKind, limit = 50) =>
    api.get<IntakePolicyVersion[]>(`/projects/${projectId}/intake/policy-versions`, {
      params: { source_kind: sourceKind, limit },
      suppressError: true,
    }),

  listDecisions: (
    projectId: number,
    filters: { executed?: boolean; decision?: string; limit?: number } = {},
  ) =>
    api.get<IntakeDecision[]>(`/projects/${projectId}/intake/decisions`, {
      params: {
        executed: filters.executed === undefined ? undefined : filters.executed ? 'true' : 'false',
        decision: filters.decision,
        limit: filters.limit ?? 200,
      },
      suppressError: true,
    }),

  getCompletionSettings: (projectId: number) =>
    api.get<CompletionSettings>(`/projects/${projectId}/completion/settings`, {
      suppressError: true,
    }),
}

/** PUT body row. */
export interface IntakeSettingInput {
  source_kind: IntakeSourceKind
  mode: IntakeMode
  daily_cap: number
  allow_list: string[]
  bot_list: string[]
}
