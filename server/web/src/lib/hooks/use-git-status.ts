import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { DiffFileStatus } from './use-diff';

interface GitStatusResponse {
  modified: string[];
  added: string[];
  deleted: string[];
  untracked: string[];
}

export function useGitStatus(repoId: number | null, workspaceId?: string | null) {
  return useQuery({
    queryKey: workspaceId
      ? ['workspace', workspaceId, 'repositories', repoId, 'git', 'status']
      : ['repositories', repoId, 'git', 'status'],
    queryFn: () => {
      const params = workspaceId ? `?workspace_id=${workspaceId}` : '';
      return api.get<GitStatusResponse>(
        `/repositories/${repoId}/git/status${params}`
      );
    },
    enabled: !!repoId,
    retry: 1,
  });
}

// Transform flat status arrays into StatusFile objects for uniform rendering
export interface StatusFile {
  path: string;
  status: DiffFileStatus;
}

export function useGitStatusFiles(repoId: number | null, workspaceId?: string | null) {
  const { data, isLoading, error, refetch } = useGitStatus(repoId, workspaceId);

  const files: StatusFile[] = [
    ...(data?.modified.map((p) => ({ path: p, status: 'modified' as DiffFileStatus })) || []),
    ...(data?.added.map((p) => ({ path: p, status: 'added' as DiffFileStatus })) || []),
    ...(data?.deleted.map((p) => ({ path: p, status: 'deleted' as DiffFileStatus })) || []),
    ...(data?.untracked.map((p) => ({ path: p, status: 'untracked' as DiffFileStatus })) || []),
  ];

  return { files, isLoading, error, refetch };
}
