import { api, apiFetch } from '@/lib/api'

// Typed client for the Intake / Completion endpoints defined in
// docs/superpowers/specs/2026-11-03-intake-completion-autonomy-design.md §6.9.

export type IntakeMode = 'off' | 'observe' | 'auto'
export type IntakeSourceKind = 'im_bot' | 'github' | 'ci' | 'email' | 'webhook'
export type IntakeStrength = 'explicit' | 'inferred' | 'denied'
export type IntakeDecisionKind = 'accept' | 'resume' | 'ignore' | 'unavailable'

// The stored would_do_json is a Go-marshaled intake.WouldDo, so its TaskBrief
// keys are the Go field names. Keeping the wire shape here avoids a second
// representation and keeps old decisions readable.
export interface IntakeBriefDraft {
  Goal: string
  Acceptance: string[]
  ScopeIn: string[]
  ScopeOut: string[]
}

export interface IntakeDeliverySpec {
  SourceKind?: string
  ThreadExtID?: string
  ReplyMode?: string
}

export interface IntakeProvenance {
  DecisionID?: number
  PolicyVersion?: number
  Strength?: string
  ActorExtID?: string
  RuleIDs?: number[] | null
}

export interface IntakeBriefJson {
  Goal?: string
  Acceptance?: string[] | null
  ScopeIn?: string[] | null
  ScopeOut?: string[] | null
  Delivery?: IntakeDeliverySpec
  Provenance?: IntakeProvenance
}

export interface IntakeWouldDo {
  decision?: IntakeDecisionKind
  brief?: IntakeBriefJson | null
  thread?: { ThreadExtID?: string; IssueID?: number; WorkspaceID?: number }
}

export interface IntakeEventPayload {
  ChatExtID?: string
  ThreadExtID?: string
  ActorExtID?: string
  ActorName?: string
  Text?: string
  Kind?: string
  Attachments?: { Kind: string; Name: string; ResourceID?: string }[]
}

export interface IntakeDecision {
  id: number
  event_id: number
  project_id: number
  source_kind: IntakeSourceKind | string
  event_kind: string
  actor_ext_id: string
  actor_name: string
  thread_ext_id: string
  mode_at_decision: IntakeMode | string
  strength: IntakeStrength | string
  decision: IntakeDecisionKind | string
  executed: boolean
  reason_code: string
  reason_text: string
  matched_rule_ids: number[]
  policy_version: number
  issue_id: number | null
  workspace_id: number | null
  would_do: IntakeWouldDo
  payload: IntakeEventPayload
  created_at: string
}

export interface IntakeRule {
  id: number
  project_id: number
  source_kind: string
  name: string
  instruction: string
  enabled: boolean
}

export interface IntakeDecisionFilter {
  executed?: boolean
  decision?: IntakeDecisionKind
  limit?: number
}

export interface IntakeSourceSettings {
  source_kind: IntakeSourceKind
  connected: boolean
  mode: IntakeMode
  daily_cap: number
  rule_count: number
  allowed_member_count: number
  pending_count: number
}

export interface IntakeSettings {
  paused: boolean
  sources: IntakeSourceSettings[]
  policy_version: number | null
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
  // suppressError: the settings endpoint is part of a parallel skeleton task,
  // so a 404 must render the panel's own error state instead of a global toast.
  getSettings: (projectId: number) =>
    api.get<IntakeSettings>(`/projects/${projectId}/intake/settings`, { suppressError: true }),

  getCompletionSettings: (projectId: number) =>
    api.get<CompletionSettings>(`/projects/${projectId}/completion/settings`, {
      suppressError: true,
    }),

  // ── Calibration queue ─────────────────────────────────────────────────────

  // suppressError: the page renders an inline retry row instead of a toast on
  // every failed poll / project switch.
  listDecisions: (projectId: number, filter: IntakeDecisionFilter = {}) =>
    api.get<IntakeDecision[]>(`/projects/${projectId}/intake/decisions`, {
      params: {
        executed: filter.executed === undefined ? undefined : filter.executed ? 'true' : 'false',
        decision: filter.decision,
        limit: filter.limit,
      },
      suppressError: true,
    }),

  // suppressError: matched rule names are an enhancement — a failed rules call
  // must not blank the queue or raise a toast.
  listRules: (projectId: number) =>
    api.get<IntakeRule[]>(`/projects/${projectId}/intake/rules`, { suppressError: true }),

  saveDraft: (projectId: number, decisionId: number, brief: IntakeBriefDraft) =>
    api.patch<IntakeDecision>(
      `/projects/${projectId}/intake/decisions/${decisionId}`,
      { brief },
    ),

  accept: (projectId: number, decisionId: number, brief?: IntakeBriefDraft) =>
    api.post<IntakeDecision>(
      `/projects/${projectId}/intake/decisions/${decisionId}/accept`,
      brief ? { brief } : undefined,
    ),

  ignore: (projectId: number, decisionId: number, reasonCode: string, reasonText?: string) =>
    api.post<IntakeDecision>(
      `/projects/${projectId}/intake/decisions/${decisionId}/ignore`,
      { reason_code: reasonCode, reason_text: reasonText ?? '' },
    ),

  // suppressError: the editor maps 503/429/502 into its own localized message.
  suggestGoal: (projectId: number, decisionId: number) =>
    apiFetch<{ data: { suggestion: string } }>(
      `/projects/${projectId}/intake/decisions/${decisionId}/suggest-goal-condition`,
      { method: 'POST', suppressError: true },
    ),
}
