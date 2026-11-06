import type {
  IntakeDecision,
  IntakeMode,
  IntakeSetting,
  IntakeSettingInput,
  IntakeSettings,
  IntakeSource,
  IntakeSourceKind,
} from '@/lib/intake-api'

export const INTAKE_MODES: IntakeMode[] = ['off', 'observe', 'auto']

/** The source kinds the settings page can render, in display order. The API
 *  also returns unknown kinds; the panel renders whatever it receives. */
export const KNOWN_SOURCE_KINDS: IntakeSourceKind[] = [
  'im_bot',
  'github',
  'ci',
  'email',
  'webhook',
]

/** Where a source's connection is set up: `inline` means the panel's own
 *  dialog, the other two are sections of the settings page. */
export type ConnectTarget = 'imbot' | 'externalSources' | 'inline'

/** Sources that have a connection path in the UI. ci/email/webhook bindings are
 *  created inside the panel; im_bot and github still live in their own sections. */
export const CONNECTABLE_SOURCES: Partial<Record<IntakeSourceKind, ConnectTarget>> = {
  im_bot: 'imbot',
  github: 'externalSources',
  ci: 'inline',
  email: 'inline',
  webhook: 'inline',
}

/** Bindings grouped by source kind, preserving the server's order. */
export function bindingsByKind(
  sources: IntakeSource[] | undefined,
): Record<string, IntakeSource[]> {
  const out: Record<string, IntakeSource[]> = {}
  for (const s of sources ?? []) {
    ;(out[s.source_kind] ??= []).push(s)
  }
  return out
}

/** The most recent delivery across a set of bindings, if any ever arrived. */
export function latestEventAt(bindings: IntakeSource[] | undefined): string | null {
  let latest: string | null = null
  for (const b of bindings ?? []) {
    if (!b.last_event_at) continue
    if (latest === null || b.last_event_at > latest) latest = b.last_event_at
  }
  return latest
}

/** UI status: the server's never|ok|degraded|error (design §12). `backoff` is
 *  accepted for a response cached from before the status rename. */
export type BindingStatus = 'ok' | 'degraded' | 'error' | 'never'

export function bindingStatus(source: IntakeSource): BindingStatus {
  if (source.status === 'error') return 'error'
  if (source.status === 'degraded' || source.status === 'backoff') return 'degraded'
  if (source.status === 'never') return 'never'
  if (!source.last_event_at && source.events_24h === 0) return 'never'
  return 'ok'
}

/** Whether a binding's inbound address must be reachable from outside. */
export function isPushBinding(source: IntakeSource): boolean {
  return source.mode === 'push' || source.mode === 'inbound'
}

/** How far the inbound address can be reached from, inferred from the origin
 *  the user is browsing the app on. `public` is not proof of a reverse proxy —
 *  the UI only reminds the user to make sure one forwards to this server. */
export type InboundReachability = 'loopback' | 'private' | 'public'

function isPrivateIPv4(host: string): boolean {
  const parts = host.split('.').map(Number)
  if (parts.length !== 4 || parts.some((n) => !Number.isInteger(n) || n < 0 || n > 255)) {
    return false
  }
  const [a, b] = parts
  return (
    a === 10 ||
    (a === 172 && b >= 16 && b <= 31) ||
    (a === 192 && b === 168) ||
    (a === 169 && b === 254) ||
    a === 127
  )
}

/** Classify a browser origin (window.location.origin) by reachability. */
export function classifyInboundOrigin(origin: string): InboundReachability {
  let host: string
  try {
    host = new URL(origin).hostname.toLowerCase().replace(/^\[|\]$/g, '')
  } catch {
    return 'loopback'
  }
  if (!host) return 'loopback'
  if (host === 'localhost' || host.endsWith('.localhost')) return 'loopback'
  if (host === '::1' || host === '0.0.0.0') return 'loopback'
  if (host.endsWith('.local')) return 'private'
  if (host.includes(':')) {
    // IPv6: fc00::/7 (unique local) and fe80::/10 (link local) are private.
    if (/^f[cd][0-9a-f]{2}:/.test(host) || /^fe[89ab][0-9a-f]:/.test(host)) return 'private'
    return 'public'
  }
  if (isPrivateIPv4(host)) return 'private'
  return 'public'
}

export interface DraftSetting {
  // daily cap is kept as a string so a cleared input stays cleared while editing.
  daily_cap: string
  // enabled is the project-wide source switch; mode remains for the backend
  // (IM chats carry their own, and non-IM sources keep their stored value).
  enabled: boolean
  mode: IntakeMode
  allow_list: string[]
  bot_list: string[]
}

export type DraftMap = Record<string, DraftSetting>

export function draftFromSettings(settings: IntakeSetting[]): DraftMap {
  const out: DraftMap = {}
  for (const s of settings) {
    out[s.source_kind] = {
      enabled: s.enabled,
      mode: s.mode,
      daily_cap: String(s.daily_cap),
      allow_list: [...(s.allow_list ?? [])],
      bot_list: [...(s.bot_list ?? [])],
    }
  }
  return out
}

export function dailyCapValue(raw: string): number | null {
  const trimmed = raw.trim()
  if (!/^\d+$/.test(trimmed)) return null
  const n = Number(trimmed)
  if (!Number.isSafeInteger(n) || n < 0) return null
  return n
}

/** Field-level validation for the whole draft. Empty means save is allowed. */
export function validateDraft(draft: DraftMap): Record<string, string> {
  const errors: Record<string, string> = {}
  for (const [kind, row] of Object.entries(draft)) {
    if (dailyCapValue(row.daily_cap) === null) {
      errors[`${kind}.daily_cap`] = 'invalid'
    }
  }
  return errors
}

function sameList(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false
  const sortedA = [...a].sort()
  const sortedB = [...b].sort()
  return sortedA.every((v, i) => v === sortedB[i])
}

export function settingDiffers(draft: DraftSetting | undefined, server: IntakeSetting): boolean {
  if (!draft) return false
  const cap = dailyCapValue(draft.daily_cap)
  return (
    draft.enabled !== (server.enabled ?? true) ||
    draft.mode !== server.mode ||
    cap === null ||
    cap !== server.daily_cap ||
    !sameList(draft.allow_list, server.allow_list ?? []) ||
    !sameList(draft.bot_list, server.bot_list ?? [])
  )
}

export function changedInputs(settings: IntakeSetting[], draft: DraftMap): IntakeSettingInput[] {
  const out: IntakeSettingInput[] = []
  for (const server of settings) {
    const row = draft[server.source_kind]
    if (!row || !settingDiffers(row, server)) continue
    const cap = dailyCapValue(row.daily_cap)
    if (cap === null) continue
    out.push({
      source_kind: server.source_kind,
      enabled: row.enabled,
      mode: row.mode,
      daily_cap: cap,
      allow_list: row.allow_list,
      bot_list: row.bot_list,
    })
  }
  return out
}

export function hasChanges(settings: IntakeSettings | undefined, draft: DraftMap | null): boolean {
  if (!settings || !draft) return false
  return settings.settings.some((s) => settingDiffers(draft[s.source_kind], s))
}

/** Pending calibration = dry-run decisions a person still has to accept or
 *  ignore. Counted per source for the card action. */
export function pendingCounts(decisions: IntakeDecision[] | undefined): Record<string, number> {
  const out: Record<string, number> = {}
  for (const d of decisions ?? []) {
    if (d.executed) continue
    if (d.decision !== 'accept' && d.decision !== 'resume') continue
    out[d.source_kind] = (out[d.source_kind] ?? 0) + 1
  }
  return out
}

export interface KnownActor {
  id: string
  name: string
}

/** Distinct actors seen in recent decisions, used to seed the member picker. */
export function knownActors(decisions: IntakeDecision[] | undefined): KnownActor[] {
  const byId = new Map<string, KnownActor>()
  for (const d of decisions ?? []) {
    if (!d.actor_ext_id || byId.has(d.actor_ext_id)) continue
    byId.set(d.actor_ext_id, { id: d.actor_ext_id, name: d.actor_name || d.actor_ext_id })
  }
  return [...byId.values()]
}

export function addUnique(list: string[], value: string): string[] {
  const v = value.trim()
  if (!v || list.includes(v)) return list
  return [...list, v]
}

// ─── Policy history diff ────────────────────────────────────────────────────

interface SnapshotShape {
  mode?: string
  daily_cap?: number
  allow_list?: string[]
  bot_list?: string[]
  rules?: { id?: number; name?: string; instruction?: string; priority?: number; enabled?: boolean }[]
}

function parseSnapshot(raw: unknown): SnapshotShape {
  if (typeof raw === 'string') {
    try {
      return JSON.parse(raw) as SnapshotShape
    } catch {
      return {}
    }
  }
  if (raw && typeof raw === 'object') return raw as SnapshotShape
  return {}
}

function rulesLabel(rules: SnapshotShape['rules']): string {
  if (!rules || rules.length === 0) return '—'
  return rules
    .map((r) => `${r.name ?? `#${r.id ?? '?'}`}${r.enabled === false ? ' (off)' : ''}`)
    .join(', ')
}

export interface DiffRow {
  field: 'mode' | 'daily_cap' | 'allow_list' | 'bot_list' | 'rules'
  before: string
  after: string
  changed: boolean
}

/** Field-level diff between a policy snapshot and its predecessor. */
export function diffSnapshots(previous: unknown, current: unknown): DiffRow[] {
  const prev = parseSnapshot(previous)
  const curr = parseSnapshot(current)
  const rows: Array<Omit<DiffRow, 'changed'>> = [
    { field: 'mode', before: prev.mode ?? '—', after: curr.mode ?? '—' },
    {
      field: 'daily_cap',
      before: prev.daily_cap === undefined ? '—' : String(prev.daily_cap),
      after: curr.daily_cap === undefined ? '—' : String(curr.daily_cap),
    },
    {
      field: 'allow_list',
      before: prev.allow_list?.length ? prev.allow_list.join(', ') : '—',
      after: curr.allow_list?.length ? curr.allow_list.join(', ') : '—',
    },
    {
      field: 'bot_list',
      before: prev.bot_list?.length ? prev.bot_list.join(', ') : '—',
      after: curr.bot_list?.length ? curr.bot_list.join(', ') : '—',
    },
    { field: 'rules', before: rulesLabel(prev.rules), after: rulesLabel(curr.rules) },
  ]
  return rows.map((r) => ({ ...r, changed: r.before !== r.after }))
}
