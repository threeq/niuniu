import type { IntakeBriefDraft, IntakeDecision } from '@/lib/intake-api'

export interface BriefDraftState {
  goal: string
  acceptance: string[]
  scopeIn: string
  scopeOut: string
}

export function briefFromDecision(decision: IntakeDecision): BriefDraftState {
  const brief = decision.would_do?.brief ?? {}
  return {
    goal: brief.Goal ?? '',
    acceptance: (brief.Acceptance ?? []).filter((item) => item.trim() !== ''),
    scopeIn: (brief.ScopeIn ?? []).join(', '),
    scopeOut: (brief.ScopeOut ?? []).join(', '),
  }
}

export function briefPayload(draft: BriefDraftState): IntakeBriefDraft {
  return {
    Goal: draft.goal.trim(),
    Acceptance: draft.acceptance.map((item) => item.trim()).filter(Boolean),
    ScopeIn: splitList(draft.scopeIn),
    ScopeOut: splitList(draft.scopeOut),
  }
}

function splitList(value: string) {
  return value
    .split(',')
    .map((item) => item.trim())
    .filter(Boolean)
}
