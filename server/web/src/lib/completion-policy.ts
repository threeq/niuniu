import type { CompletionProfile, CompletionSettings } from '@/lib/intake-api'

// Completion policy resolution — the UI side of design §6.5's single rule:
// an issue's named profile wins over the project default, with no per-field
// merge. The same rule lives in server/internal/completion; both levels read
// the same authoritative rows, so the rendered summary cannot drift from what
// autohost resolves.
//
// A profile id that no longer resolves (deleted, or another project's) degrades
// to the project default — exactly what completion.Resolver.Resolve does.

export interface CompletionPolicyValues {
  autohost_enabled: boolean
  max_rounds: number
  token_budget: number
  stop_on_decision: boolean
  require_review: boolean
}

export type CompletionSource = 'project_default' | 'issue_profile'

export interface EffectiveCompletionPolicy extends CompletionPolicyValues {
  source: CompletionSource
  profile_id: number | null
  profile_name: string
}

/** Mirrors completion.DefaultPolicy() — used only before settings load. */
export const DEFAULT_COMPLETION_POLICY: CompletionPolicyValues = {
  autohost_enabled: true,
  max_rounds: 20,
  token_budget: 0,
  stop_on_decision: true,
  require_review: false,
}

function pickPolicy(source: CompletionPolicyValues): CompletionPolicyValues {
  return {
    autohost_enabled: source.autohost_enabled,
    max_rounds: source.max_rounds,
    token_budget: source.token_budget,
    stop_on_decision: source.stop_on_decision,
    require_review: source.require_review,
  }
}

export function resolveCompletionPolicy(
  settings: CompletionSettings | null | undefined,
  profiles: CompletionProfile[],
  profileId: number | null | undefined,
): EffectiveCompletionPolicy {
  if (profileId != null) {
    const profile = profiles.find((p) => p.id === profileId)
    if (profile) {
      return {
        ...pickPolicy(profile),
        source: 'issue_profile',
        profile_id: profile.id,
        profile_name: profile.name,
      }
    }
  }
  return {
    ...pickPolicy(settings ?? DEFAULT_COMPLETION_POLICY),
    source: 'project_default',
    profile_id: null,
    profile_name: '',
  }
}

// The four seeded profiles are DB rows with Chinese names. Localize those exact
// names so an English UI doesn't show Chinese; any renamed/user-created profile
// renders its own name verbatim.
const BUILTIN_PROFILE_KEYS: Record<string, string> = {
  标准: 'completion.profile.standard',
  快速: 'completion.profile.fast',
  深度: 'completion.profile.deep',
  高风险需审查: 'completion.profile.review',
}

export function completionProfileLabel(name: string, t: (key: string) => string): string {
  const key = BUILTIN_PROFILE_KEYS[name]
  return key ? t(key) : name
}

const tokenFormat = new Intl.NumberFormat('en', {
  notation: 'compact',
  maximumFractionDigits: 1,
})

export function formatTokenBudget(amount: number): string {
  return tokenFormat.format(amount)
}

/** Read-only one-line summary shared by the project and issue surfaces. */
export function completionSummary(
  policy: CompletionPolicyValues,
  t: (key: string, options?: Record<string, unknown>) => string,
): string {
  const parts = [
    policy.autohost_enabled ? t('completion.autohostOn') : t('completion.autohostOff'),
    t('completion.summaryRounds', { count: policy.max_rounds }),
    policy.token_budget > 0
      ? t('completion.budget', { amount: formatTokenBudget(policy.token_budget) })
      : t('completion.budgetUnlimited'),
    policy.stop_on_decision ? t('completion.stopOnDecisionShort') : t('completion.continueOnDecision'),
    policy.require_review ? t('completion.reviewRequired') : t('completion.reviewNotRequired'),
  ]
  return parts.join(' · ')
}
