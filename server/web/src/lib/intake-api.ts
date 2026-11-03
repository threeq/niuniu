import { api } from '@/lib/api'

// Phase 0 skeleton client for the Intake / Completion settings endpoints
// defined in docs/superpowers/specs/2026-11-03-intake-completion-autonomy-design.md §6.9.
// Read-only for now: the panels render a preview until the full policy editor lands.

export type IntakeMode = 'off' | 'observe' | 'auto'
export type IntakeSourceKind = 'im_bot' | 'github' | 'ci' | 'email' | 'webhook'

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
}
