import type { CompletionPolicyValues } from '@/lib/completion-policy'

// Editable shape shared by the project-default form and the profile dialog.
// Numbers stay strings while typing so a cleared field is invalid, not 0.
export interface CompletionPolicyDraft {
  autohost_enabled: boolean
  max_rounds: string
  token_budget: string
  stop_on_decision: boolean
  require_review: boolean
}

export const NEW_PROFILE_DRAFT: CompletionPolicyDraft = {
  autohost_enabled: true,
  max_rounds: '20',
  token_budget: '0',
  stop_on_decision: true,
  require_review: false,
}

export function draftFromPolicy(policy: CompletionPolicyValues): CompletionPolicyDraft {
  return {
    autohost_enabled: policy.autohost_enabled,
    max_rounds: String(policy.max_rounds),
    token_budget: String(policy.token_budget),
    stop_on_decision: policy.stop_on_decision,
    require_review: policy.require_review,
  }
}

const NON_NEGATIVE_INT = /^\d+$/

export function isPolicyDraftValid(draft: CompletionPolicyDraft): boolean {
  return NON_NEGATIVE_INT.test(draft.max_rounds.trim()) && NON_NEGATIVE_INT.test(draft.token_budget.trim())
}

export function policyValuesFromDraft(draft: CompletionPolicyDraft): CompletionPolicyValues {
  return {
    autohost_enabled: draft.autohost_enabled,
    max_rounds: Number(draft.max_rounds.trim()),
    token_budget: Number(draft.token_budget.trim()),
    stop_on_decision: draft.stop_on_decision,
    require_review: draft.require_review,
  }
}
