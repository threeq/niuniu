import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';

interface FileContentResponse {
  path: string;
  content: string;
  size: number;
}

export function useFileContent(
  workspaceId: string | null,
  repoId: number | null,
  path: string | null
) {
  return useQuery({
    queryKey: ['workspace', workspaceId, 'repositories', repoId, 'files', path],
    queryFn: () => api.get<FileContentResponse>(
      `/workspaces/${workspaceId}/repositories/${repoId}/files?path=${encodeURIComponent(path!)}`
    ),
    enabled: !!workspaceId && !!repoId && !!path,
    retry: 1,
  });
}
