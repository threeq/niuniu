import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { externalProxyApi } from '@/lib/external-proxy-api';
import type {
  CreateProviderBody,
  UpdateProviderBody,
} from '@/types/integration';

const KEY = ['external-providers'] as const;
const SCHEMA_KEY = (name: string) =>
  ['external-providers', 'schema', name] as const;

export function useExternalProviders() {
  return useQuery({
    queryKey: KEY,
    queryFn: externalProxyApi.listProviders,
  });
}

export function useExternalProviderSchema(name: string | undefined) {
  return useQuery({
    queryKey: SCHEMA_KEY(name ?? ''),
    queryFn: () => externalProxyApi.getProviderSchema(name!),
    enabled: Boolean(name),
  });
}

export function useCreateExternalProvider() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateProviderBody) =>
      externalProxyApi.createProvider(body),
    onSuccess: () => qc.invalidateQueries({ queryKey: KEY }),
  });
}

export function useUpdateExternalProvider() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (args: { id: number; body: UpdateProviderBody }) =>
      externalProxyApi.updateProvider(args.id, args.body),
    onSuccess: (updated) => {
      qc.invalidateQueries({ queryKey: KEY });
      qc.invalidateQueries({ queryKey: SCHEMA_KEY(updated.name) });
    },
  });
}

export function useDeleteExternalProvider() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => externalProxyApi.deleteProvider(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: KEY }),
  });
}

export function useSetProviderWriteEnabled() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (args: { id: number; enabled: boolean }) =>
      externalProxyApi.setWriteEnabled(args.id, args.enabled),
    onSuccess: () => qc.invalidateQueries({ queryKey: KEY }),
  });
}

// --- Continuous health + contract drift (#744) ------------------------------

/** Run a provider's health probe now and refresh the list. */
export function useProbeProvider() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => externalProxyApi.probeProvider(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: KEY }),
  });
}

/** Re-enable a provider after an auto-disable, clearing the failure streak. */
export function useReenableProvider() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => externalProxyApi.reenableProvider(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: KEY }),
  });
}

/** Acknowledge upstream contract drift (re-baseline the digest). */
export function useAcknowledgeProviderDrift() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => externalProxyApi.acknowledgeDrift(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: KEY }),
  });
}
