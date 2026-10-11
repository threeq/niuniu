// Team-edition integration authorization approvals (issue #741).
// Mirrors internal/api/integration_approval.go's DTO.

export type ApprovalAction = 'draft_confirm' | 'scope_relax' | 'enable_write' | 'publish'

export type ApprovalStatus =
  | 'pending'
  | 'approved'
  | 'denied'
  | 'expired'
  | 'cancelled'
  | 'invalidated'

export type ApprovalRequiredRole = 'project_admin' | 'admin' | 'owner'

export interface IntegrationApproval {
  id: number
  org_id: number
  project_id: number | null
  action: ApprovalAction
  subject_type: string
  subject_id: number
  subject_label: string
  diff: string
  summary: string
  requested_by: number
  required_role: ApprovalRequiredRole
  status: ApprovalStatus
  decided_by: number | null
  decided_at: string | null
  decision_note: string
  escalation_count: number
  expires_at: string
  created_at: string
}

export interface ProjectMember {
  project_id: number
  user_id: number
  role: string
  username: string
  display_name: string
}

export interface SubmitApprovalRequest {
  action: ApprovalAction
  project_id?: number
  subject_type?: string
  subject_id?: number
  subject_label?: string
  diff?: string
  summary?: string
}
