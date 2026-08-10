import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { Project, CreateProjectData } from '@/types/api';

export function useProjects() {
  const queryClient = useQueryClient();

  const projectsQuery = useQuery({
    queryKey: ['projects', 'active'],
    queryFn: () => api.get<Project[]>('/projects', { params: { status: 'active' } }),
    retry: 1,
  });

  const createProjectMutation = useMutation({
    mutationFn: (data: CreateProjectData) =>
      api.post<Project>('/projects', data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['projects'] });
    },
  });

  const updateProjectMutation = useMutation({
    mutationFn: ({ id, data }: { id: string; data: Partial<CreateProjectData> }) =>
      api.put<Project>(`/projects/${id}`, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['projects'] });
    },
  });

  const deleteProjectMutation = useMutation({
    mutationFn: (id: string) => api.delete<void>(`/projects/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['projects'] });
    },
  });

  return {
    projects: projectsQuery.data,
    isLoading: projectsQuery.isLoading,
    error: projectsQuery.error,
    refetch: projectsQuery.refetch,
    createProject: createProjectMutation.mutate,
    updateProject: updateProjectMutation.mutate,
    deleteProject: deleteProjectMutation.mutate,
    isCreating: createProjectMutation.isPending,
    isUpdating: updateProjectMutation.isPending,
    isDeleting: deleteProjectMutation.isPending,
  };
}

/**
 * Like useProjects but intended for sidebar stats display.
 * Accepts a status filter string (e.g. "active" or "active,hidden").
 */
export function useProjectsWithStats(status: string = 'active') {
  const projectsQuery = useQuery({
    queryKey: ['projects', status],
    queryFn: () => api.get<Project[]>('/projects', { params: { status } }),
    retry: 1,
  });

  return {
    projects: projectsQuery.data,
    isLoading: projectsQuery.isLoading,
  };
}

/**
 * Mutation to update a project's status (active/hidden).
 */
export function useUpdateProjectStatus() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ id, status }: { id: number; status: 'active' | 'hidden' }) =>
      api.put<Project>(`/projects/${id}/status`, { status }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['projects'] });
    },
  });
}

/**
 * Mutation to update a project's color (palette key or null to clear).
 * In-flight guard via `isPending` prevents racey rapid clicks.
 */
export function useUpdateProjectColor(projectId: number) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (color: string | null) =>
      api.put<Project>(`/projects/${projectId}/color`, { color }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['project', String(projectId)] });
      qc.invalidateQueries({ queryKey: ['projects'] });
    },
  });
}
