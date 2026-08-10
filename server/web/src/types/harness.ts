// Spec types
//
// Typed-config redesign (2026-05-20): the canonical config now lives on
// dedicated columns (kind / target / pattern / command / ...); the original
// `config` JSON blob is kept only for legacy rows. See server/internal/harness/spec.go.
export type HarnessKind =
  | 'regex_match'
  | 'command_exit_code'
  | 'command_output_match'
  | 'file_exists'
  | 'ai_judge'

export type HarnessTarget =
  | 'commit_message'
  | 'branch_name'
  | 'agent_output'
  | 'working_dir'
  | ''

export type HarnessThresholdOp =
  | '>='
  | '<='
  | '=='
  | '!='
  | '>'
  | '<'
  | ''

export type HarnessTrigger = 'phase_exit' | 'on_demand' | 'pre_commit' | 'on_schedule'

export const HARNESS_KINDS: HarnessKind[] = [
  'regex_match',
  'command_exit_code',
  'command_output_match',
  'file_exists',
  'ai_judge',
]

export const HARNESS_TRIGGERS: HarnessTrigger[] = [
  'phase_exit',
  'on_demand',
  'pre_commit',
  'on_schedule',
]

// Suggested judge models for ai_judge kind. Display-only — the backend accepts
// any model identifier; this is just the curated list surfaced in the dialog.
// `labelKey` maps into harness.specs.judgeModelLabels.* in settings.json so the
// short display name can be localized while the wire value stays canonical.
export const JUDGE_MODELS = [
  { value: 'claude-haiku-4-5-20251001', labelKey: 'haiku' },
  { value: 'claude-sonnet-4-6', labelKey: 'sonnet' },
  { value: 'claude-opus-4-7', labelKey: 'opus' },
] as const

export const HARNESS_THRESHOLD_OPS: Exclude<HarnessThresholdOp, ''>[] = [
  '>=',
  '<=',
  '==',
  '!=',
  '>',
  '<',
]

export interface HarnessSpec {
  id: number
  category: HarnessCategory
  name: string
  enabled: number | boolean  // SQLite stores as 0/1; resolve endpoint returns boolean
  severity: HarnessSeverity
  config: string
  created_at: string
  updated_at: string
  // Typed config fields (2026-05-20). All optional on the wire because legacy
  // rows may omit them; backend defaults kind→'regex_match', trigger_on→'phase_exit'.
  kind: HarnessKind
  // `target` is widened to string because the server may persist legacy or
  // future values the SPA doesn't know about (defensive against schema drift).
  target: HarnessTarget | string
  pattern: string
  pattern_flags: string
  command: string
  timeout_sec: number
  expected_exit_code: number
  extract_regex: string
  threshold_value: number
  threshold_op: HarnessThresholdOp | string
  file_paths: string  // JSON-encoded string array
  trigger_on: HarnessTrigger
  // ai_judge kind: judge_prompt is the rubric / instructions sent to the
  // model; judge_model is an explicit override (empty = backend default).
  // Both columns exist on every row but are only consumed when kind='ai_judge'.
  judge_prompt: string
  judge_model: string
}

export interface HarnessSpecTestResult {
  spec_id: number
  status: 'pass' | 'fail' | 'skip' | 'error'
  message: string
  details: string
  duration_ms: number
  // Set only when ai_judge actually invoked the Anthropic API. Backend uses
  // omitempty, so 0/undefined means "no cost incurred" (e.g. validation
  // failure before the API call, or non-ai_judge kinds).
  cost_usd?: number
}

export type HarnessCategory = 'commit' | 'quality' | 'workflow' | 'agent'
export type HarnessSeverity = 'error' | 'warning' | 'info'

export const HARNESS_CATEGORIES: HarnessCategory[] = ['commit', 'quality', 'workflow', 'agent']
export const HARNESS_SEVERITIES: HarnessSeverity[] = ['error', 'warning', 'info']

// Translation keys live in `common.json` under `harnessCategory` / `harnessSeverity`.
// Resolve at render time via `t(\`common:harnessCategory.${cat}\`)`. We keep
// these `*_LABEL_KEYS` mappings exported as a pure-string indirection so
// callers don't string-build the path inline (slightly easier to grep).
export const CATEGORY_LABEL_KEYS: Record<HarnessCategory, string> = {
  commit: 'common:harnessCategory.commit',
  quality: 'common:harnessCategory.quality',
  workflow: 'common:harnessCategory.workflow',
  agent: 'common:harnessCategory.agent',
}

export const SEVERITY_LABEL_KEYS: Record<HarnessSeverity, string> = {
  error: 'common:harnessSeverity.error',
  warning: 'common:harnessSeverity.warning',
  info: 'common:harnessSeverity.info',
}

// Run types — kept for Phase 2 SSE gate event payloads used by use-run-sse.ts
// and gate-progress-bar.tsx. HarnessRun / HarnessConfirmData / HarnessStatusData
// were removed along with the RunPanel / run-store subsystem.

export type HarnessRunStatus =
  | 'running'
  | 'gate_checking'
  | 'waiting_confirm'
  | 'paused'
  | 'completed'
  | 'cancelled'
  | 'failed'
  | 'gate_blocked'
  | 'stopped_max_rounds'

// ---------------------------------------------------------------------------
// Phase 2 SSE event payload types
// Source of truth: server/internal/event/types.go
// All field names are camelCase per commit 169150bd.
// ---------------------------------------------------------------------------

export type RunPhaseEventPayload = {
  runId: number;
  columnId: number;
  columnName: string;
  round?: number;
  source?: string;
  reason?: 'multi_step_forward' | 'rollback';
};

export type GateJobStartedPayload = {
  runId: number;
  jobId: number;
  columnId: number;
  specCount: number;
};

export type GateProgressPayload = {
  jobId: number;
  runId: number;
  specId: number;
  index: number;
  total: number;
  passed: boolean;
  output?: string;
};

export type GateDonePayload = {
  jobId: number;
  runId: number;
  passed: boolean;
  failureCount: number;
  durationMs: number;
};

export type AgentLifecycleAction = 'start_for_column' | 'stop_for_run';
export type AgentLifecycleStatus = 'ok' | 'skipped' | 'warn';

export type AgentLifecyclePayload = {
  runId: number;
  workspaceId: number;
  columnId?: number;
  action: AgentLifecycleAction;
  agentName?: string;
  status: AgentLifecycleStatus;
  message?: string;
};
