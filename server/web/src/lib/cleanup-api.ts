import { api, apiFetch } from '@/lib/api'
import type { CleanupPolicy, CleanupResult } from '@/types/api'

// Per-project workspace auto-cleanup policy. The sweeper deletes completed /
// not-started workspaces (and their issue) idle past inactive_days. OFF by
// default (enabled = false).
export const cleanupApi = {
  getPolicy: (projectId: number) =>
    api.get<CleanupPolicy>(`/projects/${projectId}/cleanup-policy`),

  setPolicy: (projectId: number, policy: CleanupPolicy) =>
    api.put<CleanupPolicy>(`/projects/${projectId}/cleanup-policy`, policy),

  // Manual "clean now": run one sweep regardless of the hourly schedule.
  // suppressError so only the card's targeted toast shows (apiFetch's generic
  // "API Error" would otherwise stack).
  runOnce: (projectId: number) =>
    apiFetch<CleanupResult>(`/projects/${projectId}/cleanup/run`, {
      method: 'POST',
      suppressError: true,
    }),
}
