import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { TreeNode, CreateWorktreeInput } from '@/types/api';

export function useFileTree(workspaceId: string | null) {
  const treeQuery = useQuery({
    queryKey: ['workspace-tree', workspaceId],
    queryFn: () => api.get<TreeNode>(`/workspaces/${workspaceId}/tree`),
    enabled: !!workspaceId,
    retry: 1,
  });

  return {
    tree: treeQuery.data,
    isLoading: treeQuery.isLoading,
    error: treeQuery.error,
    refetch: treeQuery.refetch,
  };
}

export function useWorkspaceRepositories(workspaceId: string | null) {
  const queryClient = useQueryClient();

  const reposQuery = useQuery({
    queryKey: ['workspace-repositories', workspaceId],
    queryFn: () => api.getWorktrees(workspaceId ? Number(workspaceId) : undefined),
    enabled: !!workspaceId,
    retry: 1,
  });

  const addWorktreeMutation = useMutation({
    mutationFn: (data: CreateWorktreeInput) => api.createWorktree(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['workspace-repositories', workspaceId] });
    },
  });

  const deleteWorktreeMutation = useMutation({
    mutationFn: (id: number) => api.deleteWorktree(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['workspace-repositories', workspaceId] });
    },
  });

  return {
    repositories: reposQuery.data,
    isLoading: reposQuery.isLoading,
    error: reposQuery.error,
    refetch: reposQuery.refetch,
    addWorktree: addWorktreeMutation.mutate,
    deleteWorktree: deleteWorktreeMutation.mutate,
    isAdding: addWorktreeMutation.isPending,
    isDeleting: deleteWorktreeMutation.isPending,
  };
}
