import { describe, it, expect } from 'vitest'
import {
  completionProfileLabel,
  completionSummary,
  resolveCompletionPolicy,
} from './completion-policy'
import type { CompletionProfile, CompletionSettings } from '@/lib/intake-api'

// The resolution rule is the one thing the UI must not get wrong: it has to
// match completion.Resolver.Resolve (issue profile > project default, never a
// per-field merge), including the dangling-reference fallback.

const settings: CompletionSettings = {
  autohost_enabled: true,
  max_rounds: 20,
  token_budget: 0,
  stop_on_decision: true,
  require_review: false,
}

function profile(overrides: Partial<CompletionProfile> = {}): CompletionProfile {
  return {
    id: 7,
    name: '快速',
    autohost_enabled: true,
    max_rounds: 3,
    token_budget: 20_000,
    stop_on_decision: true,
    require_review: false,
    issue_count: 0,
    ...overrides,
  }
}

// A translator that echoes the key so assertions don't depend on locale files.
const t = (key: string, options?: Record<string, unknown>) =>
  options ? `${key}:${JSON.stringify(options)}` : key

describe('resolveCompletionPolicy', () => {
  it('inherits the project default when the issue has no profile', () => {
    const effective = resolveCompletionPolicy(settings, [profile()], null)
    expect(effective.source).toBe('project_default')
    expect(effective.profile_id).toBeNull()
    expect(effective.max_rounds).toBe(20)
  })

  it('lets a named profile replace the project default wholesale', () => {
    const effective = resolveCompletionPolicy(settings, [profile()], 7)
    expect(effective.source).toBe('issue_profile')
    expect(effective.profile_id).toBe(7)
    expect(effective.profile_name).toBe('快速')
    // Wholesale replacement, no per-field merge.
    expect(effective.max_rounds).toBe(3)
    expect(effective.token_budget).toBe(20_000)
  })

  it('falls back to the project default when the referenced profile is gone', () => {
    const effective = resolveCompletionPolicy(settings, [], 7)
    expect(effective.source).toBe('project_default')
    expect(effective.profile_id).toBeNull()
    expect(effective.max_rounds).toBe(20)
  })

  it('falls back to schema defaults before settings load', () => {
    const effective = resolveCompletionPolicy(null, [], undefined)
    expect(effective.source).toBe('project_default')
    expect(effective.autohost_enabled).toBe(true)
    expect(effective.max_rounds).toBe(20)
    expect(effective.token_budget).toBe(0)
    expect(effective.stop_on_decision).toBe(true)
    expect(effective.require_review).toBe(false)
  })
})

describe('completionProfileLabel', () => {
  it('localizes the four seeded profile names', () => {
    expect(completionProfileLabel('标准', t)).toBe('completion.profile.standard')
    expect(completionProfileLabel('快速', t)).toBe('completion.profile.fast')
    expect(completionProfileLabel('深度', t)).toBe('completion.profile.deep')
    expect(completionProfileLabel('高风险需审查', t)).toBe('completion.profile.review')
  })

  it('keeps user-defined names verbatim', () => {
    expect(completionProfileLabel('My profile', t)).toBe('My profile')
  })
})

describe('completionSummary', () => {
  it('renders rounds, budget, stop and review', () => {
    const summary = completionSummary(profile(), t)
    expect(summary).toContain('completion.autohostOn')
    expect(summary).toContain('completion.summaryRounds')
    expect(summary).toContain('completion.budget')
    expect(summary).toContain('completion.stopOnDecisionShort')
    expect(summary).toContain('completion.reviewNotRequired')
  })

  it('marks a zero budget as unlimited and an off switch as off', () => {
    const summary = completionSummary(
      { ...settings, autohost_enabled: false, stop_on_decision: false },
      t,
    )
    expect(summary).toContain('completion.autohostOff')
    expect(summary).toContain('completion.budgetUnlimited')
    expect(summary).toContain('completion.continueOnDecision')
  })
})
