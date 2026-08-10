import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { integrationApi } from '@/lib/integration-api';
import type { ProviderName } from '@/types/integration';

// External-source bindings per project. Cache keyed by projectId so multiple
// project pages can be resident at once without colliding.
function key(projectId: number) {
  return ['external-sources', projectId] as const;
}

export function useExternalSources(projectId: number) {
  return useQuery({
    queryKey: key(projectId),
    queryFn: () => integrationApi.listSources(projectId),
    enabled: Number.isFinite(projectId) && projectId > 0,
  });
}

export function useAddSource(projectId: number) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: {
      provider: ProviderName;
      sourceKey: string;
      credentialId: number;
      config?: Record<string, unknown>;
    }) =>
      integrationApi.addSource(projectId, {
        provider: input.provider,
        source_key: input.sourceKey,
        credential_id: input.credentialId,
        config: input.config ?? {},
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: key(projectId) });
    },
  });
}

export function useDeleteSource(projectId: number) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (sourceId: number) => integrationApi.deleteSource(projectId, sourceId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: key(projectId) });
    },
  });
}
