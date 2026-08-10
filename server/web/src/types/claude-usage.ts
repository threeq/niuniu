// Types for the JSONL-aggregation backend (per spec
// docs/superpowers/specs/2026-05-02-claude-usage-jsonl-aggregation-design.md).

export type UsageSource = 'live' | 'cache'
export type UsageWindowType =
  | 'five_hour_billing'
  | 'seven_day_all'
  | 'seven_day_sonnet'
  | 'seven_day_opus'

export interface ModelUsage {
  name: string
  total_tokens: number
  cost_usd: number
}

export interface UsageWindow {
  type: UsageWindowType
  total_tokens: number
  input_tokens: number
  output_tokens: number
  cache_creation_tokens: number
  cache_read_tokens: number
  cost_usd: number
  models: ModelUsage[]
  // Only on five_hour_billing:
  block_start?: string
  block_end?: string
  is_active?: boolean
  // Anthropic-authoritative reset time, populated when the agent stream
  // has emitted a rate_limit_event. Absent when no event observed yet.
  resets_at?: string
  resets_at_captured_at?: string
}

export interface AccountUsage {
  source: UsageSource
  fetched_at: string
  next_refresh_after: string
  shared: boolean
  subscription_type?: string
  rate_limit_tier?: string
  data_source_paths: string[]
  windows: UsageWindow[]
}

// KnownClaudeUsageErrorCode mirrors the codes the BE handler emits today.
// Keep this union in sync with server/internal/api/claude_account.go GetUsage.
// Adding a new entry here forces every `switch (code)` against the union to
// either handle it or fall to the assertNever default — the compiler is your
// reminder.
export type KnownClaudeUsageErrorCode =
  | 'bad_account_id'
  | 'not_found'
  | 'projects_not_found'
  | 'claude_data_dir_not_found'
  | 'jsonl_walk_failed'
  | 'authz_error'

// Runtime field allows any string for forward-compat with future BE codes; at
// the type level prefer the narrowed `KnownClaudeUsageErrorCode` for switching.
export interface ClaudeUsageError {
  code: KnownClaudeUsageErrorCode | string
  message: string
  hint_zh?: string
}

export const KNOWN_CLAUDE_USAGE_ERROR_CODES: readonly KnownClaudeUsageErrorCode[] = [
  'bad_account_id',
  'not_found',
  'projects_not_found',
  'claude_data_dir_not_found',
  'jsonl_walk_failed',
  'authz_error',
] as const

export function isKnownClaudeUsageErrorCode(c: string): c is KnownClaudeUsageErrorCode {
  return (KNOWN_CLAUDE_USAGE_ERROR_CODES as readonly string[]).includes(c)
}
