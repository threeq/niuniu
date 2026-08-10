import { api, apiFetch } from '@/lib/api'
import type { Memory, MemoryDetail, MemoryVersion, MemorySweepRun, ExtractStatus } from '@/types/api'
import type { OwnerRef } from '@/types/org'

export interface CreateMemoryInput {
  project_id?: number
  owner?: OwnerRef
  mem_type: string
  title: string
  content: string
  source_path?: string
}

export interface UpdateMemoryInput {
  mem_type: string
  title: string
  content: string
  source_path?: string
}

export const memoryApi = {
  listForProject: (projectId: number, type?: string) => {
    const params = new URLSearchParams({ project_id: String(projectId) })
    if (type) params.set('type', type)
    return api.get<Memory[]>(`/memories?${params.toString()}`)
  },

  get: (id: number) => api.get<MemoryDetail>(`/memories/${id}`),

  create: (data: CreateMemoryInput) => api.post<Memory>('/memories', data),

  update: (id: number, data: UpdateMemoryInput) => api.put<Memory>(`/memories/${id}`, data),

  delete: (id: number, hard = false) =>
    api.delete<void>(`/memories/${id}${hard ? '?hard=1' : ''}`),

  restore: (id: number) => api.post<Memory>(`/memories/${id}/restore`, {}),

  versions: (id: number) => api.get<MemoryVersion[]>(`/memories/${id}/versions`),

  rollback: (id: number, version: number) =>
    api.post<Memory>(`/memories/${id}/rollback`, { version }),

  // Server-side AI extraction over a workspace's recent session; writes memories.
  // Runs asynchronously — returns immediately with {running:true}; poll
  // extractStatus for completion. The endpoint path is retained for client compat.
  extractSession: (workspaceId: number) =>
    api.post<{ data: { running: boolean } }>(`/workspaces/${workspaceId}/extract-learnings`, {}),

  // Poll async extraction progress for a workspace (drives the spinner button).
  extractStatus: (workspaceId: number) =>
    api.get<{ data: ExtractStatus }>(`/workspaces/${workspaceId}/extract-learnings/status`),

  // --- Automatic memory-staleness management (per project) ---

  // Execution log of automatic staleness sweeps for a project.
  listSweepRuns: (projectId: number) =>
    api.get<MemorySweepRun[]>(`/projects/${projectId}/memory-sweep-runs`),
  // Clear a project's automatic-maintenance run log. suppressError so the hook's
  // targeted toast is the only one (apiFetch's generic "API Error" would stack).
  clearSweepRuns: (projectId: number) =>
    apiFetch<{ cleared: boolean }>(`/projects/${projectId}/memory-sweep-runs`, {
      method: 'DELETE',
      suppressError: true,
    }),

  // Review queue: memories soft-deleted by detection, awaiting keep/delete.
  listReviewQueue: (projectId: number) =>
    api.get<Memory[]>(`/projects/${projectId}/memory-review-queue`),

  // Per-project automatic-maintenance schedule (cron expression). An empty
  // string means the feature is OFF (the default).
  getSchedule: (projectId: number) =>
    api.get<{ cron: string }>(`/projects/${projectId}/memory-schedule`),
  setSchedule: (projectId: number, cron: string) =>
    api.put<{ cron: string; enabled: boolean }>(`/projects/${projectId}/memory-schedule`, { cron }),

  // Manual "run once": trigger a single maintenance run regardless of schedule.
  // suppressError so only the hook's targeted toast (which carries the backend's
  // specific message) shows — apiFetch's generic "API Error" would otherwise stack.
  runMaintenanceOnce: (projectId: number) =>
    apiFetch<{ started: boolean; issue_id: number }>(`/projects/${projectId}/memory-maintenance/run`, {
      method: 'POST',
      suppressError: true,
    }),

  // Keep a queued memory: restores it and clears the review flag.
  keepMemory: (id: number) => api.post<void>(`/memories/${id}/keep`, {}),

  // Confirm deletion of a queued memory: clears the flag, stays soft-deleted.
  confirmDeleteMemory: (id: number) => api.post<void>(`/memories/${id}/confirm-delete`, {}),
}
