import { api } from '@/lib/api'

// Mission control (design §6.6 / §6.7.8): two independent metric panels —
// Intake (任务来源) and Completion (自动完成). They are deliberately separate
// payloads so the UI cannot collapse them into one "autonomy score".

export type MissionControlUnit = 'count' | 'percent' | 'minutes' | 'rounds' | 'tokens' | 'usd'
export type MissionControlStatus = 'ok' | 'warning' | 'no_data'

export interface MissionControlMetric {
  key: string
  /** null means "no data" and must render as "—", never 0. */
  value: number | null
  unit: MissionControlUnit
  status: MissionControlStatus
  /** Where a click drills to: intake | intake_source | workspaces | tokens. */
  drill: string
  num?: number
  denom?: number
}

export interface IntakeDailyCount {
  source_kind: string
  thread_ext_id?: string
  day: string
  count: number
}

/** Delivery health of one ci/email/webhook binding (design §12). Durable
 *  fields come from intake_events/decisions; the 401/400/duplicate signals are
 *  kept in memory, so a duplicate_24h of 0 after a restart is expected. */
export type IntakeSourceHealthStatus = 'never' | 'ok' | 'degraded' | 'error'

export interface IntakeSourceHealth {
  source_id: number
  project_id: number
  name: string
  source_kind: string
  provider: string
  mode: string
  status: IntakeSourceHealthStatus
  last_success_at?: string
  last_failure_at?: string
  last_error?: string
  backoff_until?: string
  success_24h: number
  duplicate_24h: number
  rejected_24h: number
  consecutive_failures: number
  /** secret_misconfigured | payload_mismatch | imap_auth_failed */
  alert?: string
  alert_reason?: string
}

export interface MissionControlIntake {
  paused: boolean
  project_count: number
  metrics: MissionControlMetric[]
  per_source: IntakeDailyCount[]
  per_chat: IntakeDailyCount[]
  sources: IntakeSourceHealth[]
}

export interface MissionControlCompletion {
  metrics: MissionControlMetric[]
}

export interface MissionControlView {
  intake: MissionControlIntake
  completion: MissionControlCompletion
}

export const missionControlApi = {
  get: (owner?: string | null) =>
    api.get<MissionControlView>('/mission-control', {
      params: { owner: owner ?? undefined },
      suppressError: true,
    }),

  setPaused: (paused: boolean, owner?: string | null) => {
    const suffix = owner ? `?owner=${encodeURIComponent(owner)}` : ''
    return api.put<{ paused: boolean; projects: number }>(`/mission-control/pause${suffix}`, {
      paused,
    })
  },
}
