import type {
  IntakeDecision,
  IntakeMode,
  IntakeSetting,
  IntakeSettingInput,
  IntakeSettings,
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

/** Sources that have a connection path in the UI today. The rest render as
 *  "not connected / coming soon" instead of pretending to be ready. */
export const CONNECTABLE_SOURCES: Partial<Record<IntakeSourceKind, 'imbot' | 'externalSources'>> = {
  im_bot: 'imbot',
  github: 'externalSources',
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
