import { apiFetch } from './api'

export interface GateSpecBrief {
  id: number
  name: string
  severity: string
}

export interface ColumnExtension {
  id: number
  project_id: number
  name: string
  position: number
  lifecycle_mapping: string
  // Backend DTO uses *string for these fields, so JSON carries `null` when the
  // column has no agent / prompt configured. Treating them as bare `string`
  // caused `.trim()` crashes in the editor dialog.
  reviewer_agent: string | null
  phase_prompt: string | null
  // Backend serializes the int64 column directly (0/1), not a boolean.
  auto_advance: number
  gate_specs: GateSpecBrief[]
  // AI-native column model (spec §4). op_primitive: 'none' | 'instruct' | 'complete'.
  // when_to_use is an AI-generated routing hint, editable; null = unset.
  op_primitive: string
  when_to_use: string | null
}

// The reshaped column editor only sends the AI-native fields. reviewer_agent /
// auto_advance are omitted, so the backend leaves them unchanged (pointer-nil
// semantics). op_instruction reuses the phase_prompt column. (executor_agent is
// retired and no longer part of the column model.)
export interface UpdateColumnExtensionBody {
  op_primitive: string
  op_instruction: string | null
  when_to_use: string | null
}

export const columnExtensionApi = {
  update: (columnId: number, body: UpdateColumnExtensionBody) =>
    apiFetch<ColumnExtension>(`/columns/${columnId}/extension`, {
      method: 'PUT',
      body: JSON.stringify({
        op_primitive: body.op_primitive,
        phase_prompt: body.op_instruction,
        when_to_use: body.when_to_use,
      }),
    }),
}

export type GateApplicability = 'if_routed' | 'always'

export interface ColumnGateSpecLink {
  column_id: number
  spec_id: number
  position: number
  applicability: GateApplicability
}

export interface GateSpecBindingInput {
  spec_id: number
  applicability: GateApplicability
}

export const columnGateSpecApi = {
  list: (columnId: number) =>
    apiFetch<{ specs: ColumnGateSpecLink[] }>(`/columns/${columnId}/gate-specs`),
  // Applicability-aware bind: each spec carries if_routed (column-level gate) or
  // always (project floor). Spec section 5/18.
  replace: (columnId: number, gates: GateSpecBindingInput[]) =>
    apiFetch<{ specs: ColumnGateSpecLink[] }>(`/columns/${columnId}/gate-specs`, {
      method: 'PUT',
      body: JSON.stringify({ gates }),
    }),
}

