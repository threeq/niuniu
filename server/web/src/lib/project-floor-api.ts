import { api } from '@/lib/api'
import type { ProjectFloor, ProjectFloorSummary } from '@/types/api'

// Per-project 底线 (floor) command: the single build/test command that must exit 0
// before an issue may complete. Empty command = no floor (completion is not gated).
export const projectFloorApi = {
  get: (projectId: number) => api.get<ProjectFloor>(`/projects/${projectId}/floor`),

  set: (projectId: number, floor: ProjectFloor) =>
    api.put<ProjectFloor>(`/projects/${projectId}/floor`, floor),

  // Cross-project overview for the engineering-standards page. Served under
  // /harness because /projects/:id already owns that slot in the route tree.
  list: (projectIds: number[]) =>
    api.get<ProjectFloorSummary[]>('/harness/project-floors', {
      params: { ids: projectIds.join(',') },
    }),
}
