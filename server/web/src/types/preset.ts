// Types for preset-related API responses and operations.
// Mirrors the Go structs in server/internal/harness/default_templates.go.

export type ImportStrategy =
  | 'fail_if_not_empty'
  | 'merge_skip_conflict'
  | 'overwrite'

export interface PresetColumn {
  name: string
  color: string
  position: number
  reviewer_agent: string
  phase_prompt: string
  auto_advance: boolean
  gate_spec_names: string[]
  lifecycle_stage: string
}

export interface PresetSpec {
  name: string
  description: string
  check_type: string
  check_config: string
}

export interface PresetTemplate {
  name: string
  icon: string
  description: string
  kickoff_prompt: string
  max_rounds: number
  is_default: boolean
  column_sequence: string[]
}

export interface Preset {
  name: string
  label: string
  label_en: string
  description: string
  icon: string
  columns: PresetColumn[]
  specs: PresetSpec[]
  templates: PresetTemplate[]
}

export interface ImportResultCounts {
  columns_count: number
  templates_count: number
  specs_count: number
  gate_specs_count: number
}

export interface ImportResultSkipped {
  columns: string[]
  templates: string[]
  specs: string[]
}

export interface ImportResult {
  imported: ImportResultCounts
  skipped: ImportResultSkipped
}
