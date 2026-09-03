import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { GitFileDiff } from './use-file-diff';

// Diff file status types
export type DiffFileStatus = 'modified' | 'added' | 'deleted' | 'untracked' | 'renamed' | 'copied' | 'unchanged';

/**
 * A changed file, as the backend structures it. Line-level rendering consumes
 * `hunks` directly — see GitFileDiff. There is no client-side unified-diff
 * parser: the backend's git.parseDiff is the single source of truth, so the two
 * could not drift apart (they had, over the binary-file marker).
 */
export type DiffFile = GitFileDiff;

export interface WorkspaceDiff {
  workspaceId: string;
  files: DiffFile[];
  totalAdditions: number;
  totalDeletions: number;
}

interface DiffResponse {
  files: DiffFile[];
  totalAdditions: number;
  totalDeletions: number;
}

interface CreateCommentData {
  filePath: string;
  line: number;
  body: string;
}

interface Comment {
  id: string;
  filePath: string;
  line: number;
  body: string;
  createdAt: string;
}

export function useDiff(workspaceId: string | null) {
  const queryClient = useQueryClient();

  const diffQuery = useQuery({
    queryKey: ['workspace', workspaceId, 'diff'],
    queryFn: () => api.get<DiffResponse>(`/workspaces/${workspaceId}/diff`),
    enabled: !!workspaceId,
    retry: 1,
  });

  const createCommentMutation = useMutation({
    mutationFn: (data: CreateCommentData) =>
      api.post<Comment>(`/workspaces/${workspaceId}/comments`, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['workspace', workspaceId, 'diff'] });
    },
  });

  const commentsQuery = useQuery({
    queryKey: ['workspace', workspaceId, 'comments'],
    queryFn: () => api.get<Comment[]>(`/workspaces/${workspaceId}/comments`),
    enabled: !!workspaceId,
  });

  return {
    diff: diffQuery.data,
    files: diffQuery.data?.files || [],
    isLoading: diffQuery.isLoading,
    error: diffQuery.error,
    refetch: diffQuery.refetch,
    createComment: createCommentMutation.mutate,
    isCreatingComment: createCommentMutation.isPending,
    comments: commentsQuery.data || [],
  };
}
