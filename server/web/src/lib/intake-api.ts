import { api, apiFetch } from '@/lib/api'

// Typed client for the Intake (任务来源) / Completion (自动完成) endpoints defined
// in docs/superpowers/specs/2026-11-03-intake-completion-autonomy-design.md §6.9.

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

/** One dry-run decision, joined with its originating event. */
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
  /** Queue state derived by the server: pending | accepted | ignored. */
  status?: 'pending' | 'accepted' | 'ignored' | 'consumed'
  /** Absorbed by a later automatic execution; never processed again. */
  consumed?: boolean
  /** The task was recorded but handing the message to its agent failed. */
  delivery_failed?: boolean
  delivery_error?: string
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

export interface IntakeDecisionFilter {
  executed?: boolean
  decision?: IntakeDecisionKind
  source_kind?: IntakeSourceKind
  limit?: number
}

/** One (project, source) settings row. Kinds without a stored row come back
 *  with schema defaults (observe / cap 4 / empty lists). */
export interface IntakeSetting {
  source_kind: IntakeSourceKind
  /** Project-wide switch. IM chats still carry their own mode. */
  enabled: boolean
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

/** Per-source slice of the calibration sample. acceptance_rate is 0..1. */
export interface IntakeCalibrationSource {
  source_kind: IntakeSourceKind | string
  calibrated: number
  accepted: number
  ignored: number
  pending: number
  acceptance_rate: number
}

/** One resolved dry-run decision, listed in the enable-auto dialog. */
export interface IntakeCalibrationRecord {
  id: number
  source_kind: IntakeSourceKind | string
  actor_name: string
  actor_ext_id: string
  outcome: 'accepted' | 'ignored'
  reason_code: string
  reason_text: string
  created_at: string
}

/** GET /projects/:id/intake/calibration — the trust-ramp sample. */
export interface IntakeCalibration {
  project_id: number
  calibrated: number
  accepted: number
  ignored: number
  pending: number
  acceptance_rate: number
  min_calibrated: number
  min_acceptance_rate: number
  ready: boolean
  sources: IntakeCalibrationSource[]
  recent: IntakeCalibrationRecord[]
}

export interface IntakeRuleConditions {
  event_kind?: string
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

/** One bound GitHub repo's polling configuration and latest health. */
export interface GithubRepoConfig {
  source_key: string
  events: string[]
  last_polled_at?: string
  backoff_until?: string
  last_error?: string
}

export interface GithubConfig {
  project_id: number
  repos: GithubRepoConfig[]
  known_events: string[]
}

/** The three binding kinds Intake owns (design §3). Their provider column is the
 *  source kind; the upstream vendor lives in config.provider. */
export type IntakeBindingKind = 'ci' | 'email' | 'webhook'

/** One ci/email/webhook binding. The secret is never included; has_secret only
 *  says one is configured. Edit its secret through the one-time link. */
export interface IntakeSource {
  id: number
  project_id: number
  source_kind: IntakeBindingKind | string
  name: string
  /** push for ci/webhook, imap or inbound for email. */
  mode: string
  /** Upstream vendor (gitlab, mailgun, ...); empty for generic. */
  provider: string
  inbound_path: string
  has_secret: boolean
  /** ok | backoff | error — derived from the IMAP cursor / recent deliveries. */
  status: string
  last_event_at?: string
  events_24h: number
  /** IMAP cursor failure reason; empty for push bindings. */
  last_error?: string
  /** When polling resumes after a failure (IMAP only). */
  backoff_until?: string
  config: Record<string, unknown>
}

export interface IntakeSourceInput {
  source_kind: IntakeBindingKind
  name: string
  mode?: string
  provider?: string
  config?: Record<string, unknown>
  /** Required for email/imap: the existing imap credential to poll with. */
  credential_id?: number
}

/** Create/Rotate never return the plaintext secret: secret_link points at a
 *  page that shows it once (design §10.1). IMAP bindings have no link. */
export interface IntakeSourceWriteResult {
  source: IntakeSource
  secret_link?: string
}

/** Dry-run outcome of a synthetic delivery; nothing is recorded or executed. */
export interface IntakeTestResult {
  source_kind: string
  event_kind: string
  actor_ext_id: string
  thread_ext_id: string
  text: string
  mode: string
  paused: boolean
  strength: string
  reason_code: string
  allowed: boolean
  matched_rule_ids?: number[]
  warnings?: string[]
}

export interface IntakePolicyVersion {
  id: number
  project_id: number
  source_kind: IntakeSourceKind
  version: number
  snapshot: unknown
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

/** GET/PUT /completion/settings — the project default (never a 404; absent rows
 *  report the schema defaults). Named profiles come from their own endpoint. */
export interface CompletionSettings {
  project_id?: number
  autohost_enabled: boolean
  max_rounds: number
  token_budget: number
  stop_on_decision: boolean
  require_review: boolean
  updated_at?: string
}

/** PATCH body for /completion/profiles/:pid — omitted fields keep their value. */
export interface CompletionProfileInput {
  name?: string
  autohost_enabled?: boolean
  max_rounds?: number
  token_budget?: number
  stop_on_decision?: boolean
  require_review?: boolean
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

  // suppressError: the calibration strip renders its own state, and a failed
  // poll must not raise a toast on every project switch.
  getCalibration: (projectId: number) =>
    api.get<IntakeCalibration>(`/projects/${projectId}/intake/calibration`, {
      suppressError: true,
    }),

  createRule: (projectId: number, input: IntakeRuleInput) =>
    api.post<IntakeRule>(`/projects/${projectId}/intake/rules`, input),

  updateRule: (projectId: number, ruleId: number, input: IntakeRuleInput) =>
    api.put<IntakeRule>(`/projects/${projectId}/intake/rules/${ruleId}`, input),

  deleteRule: (projectId: number, ruleId: number) =>
    api.delete<void>(`/projects/${projectId}/intake/rules/${ruleId}`),

  // GitHub Source (个人版轮询): bound repos, enabled events, poll health.
  // suppressError: the config dialog renders its own empty/error state.
  getGithubConfig: (projectId: number) =>
    api.get<GithubConfig>(`/projects/${projectId}/intake/github`, { suppressError: true }),

  updateGithubConfig: (projectId: number, repos: { source_key: string; events: string[] }[]) =>
    api.put<GithubConfig>(`/projects/${projectId}/intake/github`, { repos }),

  // ── ci / email / webhook bindings (design §10.1) ──────────────────────────

  // suppressError: the source cards render their own connected/error state, so a
  // failed poll must not raise a toast on every project switch.
  listSources: (projectId: number, sourceKind?: IntakeBindingKind) =>
    api
      .get<{ sources: IntakeSource[] }>(`/projects/${projectId}/intake/sources`, {
        params: { source_kind: sourceKind },
        suppressError: true,
      })
      .then((r) => r.sources ?? []),

  // suppressError: the connect dialog keeps the failed step open and renders the
  // server's reason inline (409 duplicate name, 400 missing credential, ...).
  createSource: (projectId: number, input: IntakeSourceInput) =>
    apiFetch<IntakeSourceWriteResult>(`/projects/${projectId}/intake/sources`, {
      method: 'POST',
      body: JSON.stringify(input),
      suppressError: true,
    }),

  // Rotate replaces the secret; the old one stops working immediately.
  // suppressError: the binding row reports the failure next to the action.
  rotateSource: (projectId: number, sourceId: number) =>
    apiFetch<IntakeSourceWriteResult>(
      `/projects/${projectId}/intake/sources/${sourceId}/rotate`,
      { method: 'POST', body: JSON.stringify({}), suppressError: true },
    ),

  // A synthetic delivery: normalized and evaluated but never recorded.
  testSource: (projectId: number, sourceId: number, sample?: Record<string, unknown> | string) =>
    apiFetch<IntakeTestResult>(`/projects/${projectId}/intake/sources/${sourceId}/test`, {
      method: 'POST',
      body: JSON.stringify({ sample }),
      suppressError: true,
    }),

  // suppressError: the binding row reports the failure next to the action.
  deleteSource: (projectId: number, sourceId: number) =>
    apiFetch<{ ok: boolean }>(`/projects/${projectId}/intake/sources/${sourceId}`, {
      method: 'DELETE',
      suppressError: true,
    }),

  // Only email/imap bindings qualify.
  pollSource: (projectId: number, sourceId: number) =>
    apiFetch<{ ok: boolean; polled: boolean }>(
      `/projects/${projectId}/intake/sources/${sourceId}/poll`,
      { method: 'POST', body: JSON.stringify({}), suppressError: true },
    ),

  listPolicyVersions: (projectId: number, sourceKind: IntakeSourceKind, limit = 50) =>
    api.get<IntakePolicyVersion[]>(`/projects/${projectId}/intake/policy-versions`, {
      params: { source_kind: sourceKind, limit },
      suppressError: true,
    }),

  getCompletionSettings: (projectId: number) =>
    api.get<CompletionSettings>(`/projects/${projectId}/completion/settings`, {
      suppressError: true,
    }),

  updateCompletionSettings: (projectId: number, input: CompletionProfileInput) =>
    api.put<CompletionSettings>(`/projects/${projectId}/completion/settings`, input),

  // suppressError: the panel renders its own inline retry state.
  listCompletionProfiles: (projectId: number) =>
    api.get<CompletionProfile[]>(`/projects/${projectId}/completion/profiles`, {
      suppressError: true,
    }),

  createCompletionProfile: (projectId: number, input: CompletionProfileInput) =>
    api.post<CompletionProfile>(`/projects/${projectId}/completion/profiles`, input),

  updateCompletionProfile: (projectId: number, profileId: number, input: CompletionProfileInput) =>
    api.patch<CompletionProfile>(`/projects/${projectId}/completion/profiles/${profileId}`, input),

  deleteCompletionProfile: (projectId: number, profileId: number) =>
    api.delete<void>(`/projects/${projectId}/completion/profiles/${profileId}`),

  // ── Calibration queue ─────────────────────────────────────────────────────

  // suppressError: the page renders an inline retry row instead of a toast on
  // every failed poll / project switch.
  listDecisions: (projectId: number, filter: IntakeDecisionFilter = {}) =>
    api.get<IntakeDecision[]>(`/projects/${projectId}/intake/decisions`, {
      params: {
        executed: filter.executed === undefined ? undefined : filter.executed ? 'true' : 'false',
        decision: filter.decision,
        source_kind: filter.source_kind,
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

  accept: (
    projectId: number,
    decisionId: number,
    brief?: IntakeBriefDraft,
    targetIssueId?: number | null,
  ) =>
    api.post<IntakeDecision>(
      `/projects/${projectId}/intake/decisions/${decisionId}/accept`,
      {
        ...(brief ? { brief } : {}),
        ...(targetIssueId ? { target_issue_id: targetIssueId } : {}),
      },
    ),

  ignore: (projectId: number, decisionId: number, reasonCode: string, reasonText?: string) =>
    api.post<IntakeDecision>(
      `/projects/${projectId}/intake/decisions/${decisionId}/ignore`,
      { reason_code: reasonCode, reason_text: reasonText ?? '' },
    ),

  // Removes the queue entry only; the event and any created task stay.
  deleteDecision: (projectId: number, decisionId: number) =>
    api.delete<{ deleted: number }>(
      `/projects/${projectId}/intake/decisions/${decisionId}`,
    ),

  // suppressError: the editor maps 503/429/502 into its own localized message.
  suggestGoal: (projectId: number, decisionId: number) =>
    apiFetch<{ data: { suggestion: string } }>(
      `/projects/${projectId}/intake/decisions/${decisionId}/suggest-goal-condition`,
      { method: 'POST', suppressError: true },
    ),
}

/** PUT body row. */
export interface IntakeSettingInput {
  source_kind: IntakeSourceKind
  enabled?: boolean
  mode: IntakeMode
  daily_cap: number
  allow_list: string[]
  bot_list: string[]
}
