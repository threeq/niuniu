import { useMemo } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { Repository, CreateRepositoryData } from '@/types/api';

export function useRepositories() {
  const queryClient = useQueryClient();

  const repositoriesQuery = useQuery({
    queryKey: ['repositories'],
    queryFn: () => api.get<Repository[]>('/repositories'),
    retry: 1,
  });

  // Order repositories alphabetically by name (dictionary order), matching the
  // project sidebar. localeCompare handles mixed Chinese/ASCII names.
  const repositories = useMemo(
    () =>
      repositoriesQuery.data
        ? [...repositoriesQuery.data].sort((a, b) => a.name.localeCompare(b.name))
        : repositoriesQuery.data,
    [repositoriesQuery.data],
  );

  const createRepositoryMutation = useMutation({
    mutationFn: (data: CreateRepositoryData) =>
      api.post<Repository>('/repositories', data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['repositories'] });
    },
  });

  const updateRepositoryMutation = useMutation({
    mutationFn: ({ id, data }: { id: string; data: Partial<CreateRepositoryData> }) =>
      api.put<Repository>(`/repositories/${id}`, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['repositories'] });
    },
  });

  const deleteRepositoryMutation = useMutation({
    mutationFn: (id: string) => api.delete<void>(`/repositories/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['repositories'] });
    },
  });

  return {
    repositories,
    isLoading: repositoriesQuery.isLoading,
    error: repositoriesQuery.error,
    refetch: repositoriesQuery.refetch,
    createRepository: createRepositoryMutation.mutate,
    updateRepository: updateRepositoryMutation.mutate,
    deleteRepository: deleteRepositoryMutation.mutate,
    isCreating: createRepositoryMutation.isPending,
    isUpdating: updateRepositoryMutation.isPending,
    isDeleting: deleteRepositoryMutation.isPending,
  };
}
