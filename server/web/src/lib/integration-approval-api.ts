import { api } from './api'
import type {
  IntegrationApproval,
  ProjectMember,
  SubmitApprovalRequest,
} from '@/types/integration-approval'

// Team-edition integration authorization approvals (issue #741).
// The backend service is the authoritative gate; these calls surface the queue
// and let a permitted member approve/deny. A 403 (NOT_PERMITTED / SELF_DECIDE)
// is the real enforcement — the UI merely hides buttons it knows are futile.
export const integrationApprovalApi = {
  // Pending requests this caller is entitled to decide (role-matched, never own).
  listMine: (): Promise<IntegrationApproval[]> =>
    api.get<IntegrationApproval[]>('/me/integration-approvals'),

  // An org's approval queue. Owner/admin only (backend enforces).
  listByOrg: (orgID: number, status?: string): Promise<IntegrationApproval[]> =>
    api.get<IntegrationApproval[]>(`/orgs/${orgID}/integration-approvals`, {
      params: { status },
    }),

  submit: (orgID: number, data: SubmitApprovalRequest): Promise<IntegrationApproval> =>
    api.post<IntegrationApproval>(`/orgs/${orgID}/integration-approvals`, data),

  decide: (
    orgID: number,
    requestID: number,
    approve: boolean,
    note?: string
  ): Promise<IntegrationApproval> =>
    api.post<IntegrationApproval>(
      `/orgs/${orgID}/integration-approvals/${requestID}/decision`,
      { approve, note }
    ),

  // Project-level roles (project admin).
  listProjectMembers: (projectID: number): Promise<ProjectMember[]> =>
    api.get<ProjectMember[]>(`/projects/${projectID}/members`),

  grantProjectMember: (projectID: number, userID: number): Promise<void> =>
    api.post<void>(`/projects/${projectID}/members`, { user_id: userID }),

  revokeProjectMember: (projectID: number, userID: number): Promise<void> =>
    api.delete<void>(`/projects/${projectID}/members/${userID}`),
}
