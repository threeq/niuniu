import { describe, expect, it } from 'vitest'
import type { IntakeDecision, IntakeSetting } from '@/lib/intake-api'
import {
  changedInputs,
  dailyCapValue,
  diffSnapshots,
  draftFromSettings,
  hasChanges,
  pendingCounts,
  validateDraft,
} from './intake-utils'

function setting(over: Partial<IntakeSetting> = {}): IntakeSetting {
  return {
    source_kind: 'im_bot',
    mode: 'observe',
    daily_cap: 4,
    allow_list: ['ou_alice'],
    bot_list: [],
    ...over,
  }
}

function decision(over: Partial<IntakeDecision> = {}): IntakeDecision {
  return {
    id: 1,
    event_id: 1,
    project_id: 1,
    source_kind: 'im_bot',
    event_kind: 'message',
    actor_ext_id: 'ou_alice',
    actor_name: 'Alice',
    thread_ext_id: '',
    mode_at_decision: 'observe',
    strength: 'explicit',
    decision: 'accept',
    executed: false,
    reason_code: 'direct_request',
    reason_text: '',
    matched_rule_ids: [],
    policy_version: 1,
    issue_id: null,
    workspace_id: null,
    would_do: {},
    payload: {},
    created_at: '2026-11-03T00:00:00Z',
    ...over,
  }
}

describe('intake draft helpers', () => {
  it('only emits rows that actually changed', () => {
    const server = setting()
    const draft = draftFromSettings([server])
    draft.im_bot.mode = 'auto'
    const rows = changedInputs([server], draft)
    expect(rows).toHaveLength(1)
    expect(rows[0]).toMatchObject({ source_kind: 'im_bot', mode: 'auto', daily_cap: 4 })
  })

  it('treats reordered lists as unchanged', () => {
    const server = setting({ allow_list: ['a', 'b'] })
    const draft = draftFromSettings([server])
    draft.im_bot.allow_list = ['b', 'a']
    expect(hasChanges({ project_id: 1, paused: false, settings: [server] }, draft)).toBe(false)
  })

  it('rejects non-integer and negative daily caps', () => {
    expect(dailyCapValue('0')).toBe(0)
    expect(dailyCapValue('4')).toBe(4)
    expect(dailyCapValue('')).toBeNull()
    expect(dailyCapValue('-1')).toBeNull()
    expect(dailyCapValue('1.5')).toBeNull()

    const draft = draftFromSettings([setting()])
    draft.im_bot.daily_cap = '-2'
    expect(validateDraft(draft)).toHaveProperty('im_bot.daily_cap')
  })
})

describe('pendingCounts', () => {
  it('counts only unexecuted accept/resume decisions per source', () => {
    const counts = pendingCounts([
      decision({ id: 1 }),
      decision({ id: 2, source_kind: 'github', decision: 'resume' }),
      decision({ id: 3, decision: 'ignore' }),
      decision({ id: 4, executed: true }),
      decision({ id: 5, decision: 'accept' }),
    ])
    expect(counts).toEqual({ im_bot: 2, github: 1 })
  })
})

describe('diffSnapshots', () => {
  it('marks changed fields and leaves unchanged ones muted', () => {
    const rows = diffSnapshots(
      { mode: 'observe', daily_cap: 4, allow_list: ['a'], bot_list: [], rules: [] },
      {
        mode: 'auto',
        daily_cap: 6,
        allow_list: ['a'],
        bot_list: ['bot'],
        rules: [{ id: 1, name: '发布', enabled: true }],
      },
    )
    expect(rows.find((r) => r.field === 'mode')).toMatchObject({ changed: true, after: 'auto' })
    expect(rows.find((r) => r.field === 'allow_list')).toMatchObject({ changed: false })
    expect(rows.find((r) => r.field === 'rules')?.after).toContain('发布')
  })

  it('handles a first version with no predecessor', () => {
    const rows = diffSnapshots(undefined, { mode: 'observe', daily_cap: 4 })
    expect(rows.find((r) => r.field === 'mode')).toMatchObject({
      before: '—',
      after: 'observe',
      changed: true,
    })
  })
})
