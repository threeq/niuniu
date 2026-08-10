import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { Issue, CreateIssueData } from '@/types/api';

export interface MoveIssueData {
  column_id: number;
  position: number;
}

export function useIssues(columnId: string | null) {
  const queryClient = useQueryClient();

  const issuesQuery = useQuery({
    queryKey: ['issues', columnId],
    queryFn: () => api.get<Issue[]>(`/columns/${columnId}/issues`),
    retry: 1,
    enabled: !!columnId,
  });

  const createIssueMutation = useMutation({
    mutationFn: (data: CreateIssueData) =>
      api.post<Issue>(`/columns/${columnId}/issues`, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['issues', columnId] });
    },
  });

  const updateIssueMutation = useMutation({
    mutationFn: ({ id, data }: { id: string; data: Partial<CreateIssueData> }) =>
      api.put<Issue>(`/issues/${id}`, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['issues', columnId] });
    },
  });

  const deleteIssueMutation = useMutation({
    mutationFn: (id: string) => api.delete<void>(`/issues/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['issues', columnId] });
    },
  });

  return {
    issues: issuesQuery.data,
    isLoading: issuesQuery.isLoading,
    error: issuesQuery.error,
    refetch: issuesQuery.refetch,
    createIssue: createIssueMutation.mutate,
    updateIssue: updateIssueMutation.mutate,
    deleteIssue: deleteIssueMutation.mutate,
    isCreating: createIssueMutation.isPending,
    isUpdating: updateIssueMutation.isPending,
    isDeleting: deleteIssueMutation.isPending,
  };
}

export function useMoveIssue() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ issueId, data }: { issueId: string; data: MoveIssueData }) =>
      api.put<Issue>(`/issues/${issueId}/move`, data),
    onMutate: async () => {
      // Cancel any outgoing refetches
      await queryClient.cancelQueries({ queryKey: ['issues'] });

      // Snapshot the previous values for all columns
      const previousIssuesMap: Record<string, Issue[]> = {};

      // Get all queries that start with 'issues'
      const queries = queryClient.getQueriesData<Issue[]>({ queryKey: ['issues'] });
      queries.forEach(([queryKey, issues]) => {
        if (issues !== undefined) {
          previousIssuesMap[JSON.stringify(queryKey)] = issues;
        }
      });

      return { previousIssuesMap };
    },
    onError: (_err, _variables, context) => {
      // Rollback on error
      if (context?.previousIssuesMap) {
        Object.entries(context.previousIssuesMap).forEach(([queryKey, issues]) => {
          queryClient.setQueryData<Issue[]>(JSON.parse(queryKey), issues);
        });
      }
    },
    onSettled: () => {
      // Always refetch after error or success to ensure consistency
      queryClient.invalidateQueries({ queryKey: ['issues'] });
    },
  });
}
