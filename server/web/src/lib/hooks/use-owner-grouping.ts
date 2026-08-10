import { useMemo } from 'react';
import { useAuthStore } from '@/stores/auth-store';
import { useOrgStore } from '@/stores/org-store';
import type { OwnerRef } from '@/types/org';

export type OwnerGroupingResult<T> =
  | { mode: 'flat'; items: T[] }
  | {
      mode: 'grouped';
      groups: Array<{
        key: string;
        label: string;
        icon: 'user' | 'org';
        items: T[];
      }>;
    };

export interface OrgLite {
  id: number;
  name: string;
}

// Pure function — exported for unit tests.
export function computeOwnerGrouping<T extends { owner?: OwnerRef }>(
  items: T[],
  currentUserId: number,
  myOrgs: OrgLite[],
): OwnerGroupingResult<T> {
  const ownerKey = (o: OwnerRef | undefined): string =>
    o ? `${o.type}:${o.id}` : 'unknown';
  const distinctOwners = new Set(items.map((it) => ownerKey(it.owner)));

  // Suppress when nothing useful to show.
  if (myOrgs.length === 0) return { mode: 'flat', items };
  if (
    myOrgs.length === 1 &&
    distinctOwners.size <= 1 &&
    items.every((it) => it.owner?.type === 'org' && it.owner.id === myOrgs[0].id)
  ) {
    return { mode: 'flat', items };
  }

  const buckets = new Map<string, T[]>();
  for (const it of items) {
    const k = ownerKey(it.owner);
    const arr = buckets.get(k);
    if (arr) arr.push(it);
    else buckets.set(k, [it]);
  }

  const groups: Array<{ key: string; label: string; icon: 'user' | 'org'; items: T[] }> = [];

  // Personal first.
  const personalKey = `user:${currentUserId}`;
  const personalItems = buckets.get(personalKey) ?? [];
  if (personalItems.length > 0) {
    groups.push({ key: personalKey, label: 'Personal', icon: 'user', items: personalItems });
    buckets.delete(personalKey);
  }

  // Orgs alphabetically.
  const orgGroups = Array.from(buckets.entries())
    .filter(([k]) => k.startsWith('org:'))
    .map(([k, arr]) => {
      const id = Number(k.slice(4));
      const org = myOrgs.find((o) => o.id === id);
      return {
        key: k,
        label: org?.name ?? `Org #${id}`,
        icon: 'org' as const,
        items: arr,
      };
    })
    .sort((a, b) => a.label.localeCompare(b.label));
  groups.push(...orgGroups);

  // Anything left (foreign user owners — shouldn't happen in practice; if it does, append at the end).
  for (const [k, arr] of buckets.entries()) {
    if (k.startsWith('org:')) continue;
    groups.push({ key: k, label: k, icon: 'user', items: arr });
  }

  return { mode: 'grouped', groups };
}

// React hook wrapping the pure function with stores.
export function useOwnerGrouping<T extends { owner?: OwnerRef }>(items: T[]): OwnerGroupingResult<T> {
  const currentUserId = useAuthStore((s) => s.user?.id ?? 0);
  const myOrgs = useOrgStore((s) => s.myOrgs);
  return useMemo(() => computeOwnerGrouping(items, currentUserId, myOrgs), [items, currentUserId, myOrgs]);
}

// Helper: should the workspace item show `项目@组织` suffix for this owner?
// Same suppression rules as the grouping hook.
export function shouldShowOwnerSuffix(
  ownerType: string | undefined,
  ownerID: number | undefined,
  currentUserId: number,
  myOrgs: OrgLite[],
): boolean {
  if (!ownerType || ownerID == null) return false;
  if (ownerType === 'user' && ownerID === currentUserId) return false; // personal
  if (myOrgs.length === 0) return false;
  if (myOrgs.length === 1 && ownerType === 'org' && ownerID === myOrgs[0].id) return false;
  return true;
}
