import { apiFetch } from './api'
import type {
  HarnessSpec,
  HarnessSpecTestResult,
} from '@/types/harness'

// Spec API
export const harnessSpecApi = {
  listGlobal: () => apiFetch<HarnessSpec[]>('/harness/specs'),
  resolve: () => apiFetch<HarnessSpec[]>('/harness/specs/resolve'),
  get: (id: number) => apiFetch<HarnessSpec>(`/harness/specs/${id}`),
  create: (data: Record<string, unknown>) =>
    apiFetch<HarnessSpec>('/harness/specs', { method: 'POST', body: JSON.stringify(data) }),
  update: (id: number, data: Record<string, unknown>) =>
    apiFetch<void>(`/harness/specs/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  delete: (id: number) =>
    apiFetch<void>(`/harness/specs/${id}`, { method: 'DELETE' }),
  // On-demand check execution. Body fields are all optional; backend
  // populates the unset ones with empty strings before invoking the checker.
  test: (
    id: number,
    body?: {
      commit_message?: string
      branch_name?: string
      agent_output?: string
      workspace_path?: string
    },
  ) =>
    apiFetch<HarnessSpecTestResult>(`/harness/specs/${id}/test`, {
      method: 'POST',
      body: JSON.stringify(body ?? {}),
    }),
}

