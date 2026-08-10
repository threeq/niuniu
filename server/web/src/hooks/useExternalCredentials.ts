import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { integrationApi } from '@/lib/integration-api';
import type {
  ProviderName,
  CreateCredentialBody,
  PatchCredentialBody,
} from '@/types/integration';

export function useExternalCredentials(provider?: ProviderName) {
  return useQuery({
    queryKey: provider ? ['external-credentials', provider] : ['external-credentials'],
    queryFn: () => integrationApi.listCredentials(provider),
  });
}

export function useCreateCredential() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateCredentialBody) => integrationApi.createCredential(body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['external-credentials'] }),
  });
}

export function usePatchCredential() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (args: { id: number; body: PatchCredentialBody }) =>
      integrationApi.patchCredential(args.id, args.body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['external-credentials'] }),
  });
}

export function useVerifyImapCredential() {
  return useMutation({
    mutationFn: (id: number) => integrationApi.verifyImap(id),
  });
}

export function useDeleteCredential() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => integrationApi.deleteCredential(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['external-credentials'] }),
  });
}

const USAGES_KEY = (id: number) =>
  ['external-credentials', id, 'usages'] as const;

export function useCredentialUsages(id: number | undefined, enabled = true) {
  return useQuery({
    queryKey: USAGES_KEY(id ?? 0),
    queryFn: () => integrationApi.credentialUsages(id!),
    enabled: Boolean(id) && enabled,
  });
}

export function useDeleteSourceBinding() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (args: { projectId: number; sourceId: number }) =>
      integrationApi.deleteSource(args.projectId, args.sourceId),
    onSuccess: () => {
      // Both the usages list for any credential and any project's
      // sources may have changed.
      qc.invalidateQueries({ queryKey: ['external-credentials'] });
    },
  });
}
