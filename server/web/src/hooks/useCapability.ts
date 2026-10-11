import { useQuery } from '@tanstack/react-query';
import { capabilityApi } from '@/lib/capability-api';
import type { CapabilityTargetKind } from '@/types/capability';

const KEY = (kind: CapabilityTargetKind, ref: string) =>
  ['capability', kind, ref] as const;

/** Fetch a provider's or data source's operable capability description. */
export function useCapability(
  kind: CapabilityTargetKind,
  ref: string | number | undefined,
  enabled: boolean,
) {
  return useQuery({
    queryKey: KEY(kind, String(ref ?? '')),
    queryFn: () => capabilityApi.get(kind, ref!),
    enabled: enabled && ref !== undefined && ref !== '',
  });
}
