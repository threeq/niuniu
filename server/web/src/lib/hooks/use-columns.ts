import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { Column, CreateColumnData } from '@/types/api';

export function useColumns(projectId: number | null) {
  const queryClient = useQueryClient();

  const columnsQuery = useQuery({
    queryKey: ['columns', projectId],
    queryFn: () => api.get<Column[]>(`/projects/${projectId}/columns`),
    retry: 1,
    enabled: !!projectId,
  });

  const createColumnMutation = useMutation({
    mutationFn: (data: CreateColumnData) =>
      api.post<Column>(`/projects/${projectId}/columns`, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['columns', projectId] });
    },
  });

  const updateColumnMutation = useMutation({
    mutationFn: ({ id, data }: { id: number; data: Partial<CreateColumnData> }) =>
      api.put<Column>(`/columns/${id}`, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['columns', projectId] });
    },
  });

  const moveColumnMutation = useMutation({
    mutationFn: ({ columnId, position }: { columnId: number; position: number }) =>
      api.put<void>(`/columns/${columnId}/position`, { position }),
    onMutate: async ({ columnId, position }) => {
      await queryClient.cancelQueries({ queryKey: ['columns', projectId] });
      const previous = queryClient.getQueryData<Column[]>(['columns', projectId]);
      if (previous) {
        const cols = [...previous];
        const idx = cols.findIndex(c => c.id === columnId);
        if (idx !== -1) {
          const [moved] = cols.splice(idx, 1);
          const insertAt = Math.min(position, cols.length);
          cols.splice(insertAt, 0, moved);
          const reindexed = cols.map((c, i) => ({ ...c, position: i }));
          queryClient.setQueryData(['columns', projectId], reindexed);
        }
      }
      return { previous };
    },
    onError: (_err, _vars, context) => {
      if (context?.previous) {
        queryClient.setQueryData(['columns', projectId], context.previous);
      }
    },
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: ['columns', projectId] });
    },
  });

  return {
    columns: columnsQuery.data,
    isLoading: columnsQuery.isLoading,
    error: columnsQuery.error,
    refetch: columnsQuery.refetch,
    createColumn: createColumnMutation.mutate,
    updateColumn: updateColumnMutation.mutate,
    isCreating: createColumnMutation.isPending,
    isUpdating: updateColumnMutation.isPending,
    moveColumn: moveColumnMutation.mutate,
    isMovingColumn: moveColumnMutation.isPending,
  };
}
