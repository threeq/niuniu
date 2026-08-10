import type { Org } from '@/types/org';

export interface OwnerLike {
  type: string;
  id: number;
  name?: string;
  slug?: string;
}

export interface OwnedItem {
  owner?: OwnerLike;
}

export interface OwnerGroup<T extends OwnedItem> {
  /** stable group key, e.g. "user:7" or "org:10" */
  key: string;
  /** display label for the group; empty string for the caller's personal group
   *  (consumer should fall back to an i18n string like "Personal"). */
  label: string;
  /** "user" for personal-type groups (caller's or another user's), "org" otherwise */
  ownerType: 'user' | 'org';
  /** raw owner id */
  ownerId: number;
  items: T[];
}

const ownerKey = (o: OwnerLike | undefined, fallbackUserId: number): string => {
  if (!o || !o.type || o.id <= 0) return `user:${fallbackUserId}`;
  return `${o.type}:${o.id}`;
};

/**
 * Group items by their `.owner` ref.
 *
 * Order:
 *   1. Personal group of the caller (type:"user", id == callerUserId)
 *   2. Other personal-space owners (type:"user", id != callerUserId), by id ascending
 *   3. Member orgs in `myOrgs` order
 *   4. Unknown orgs (non-member) appended last, by id ascending
 *
 * Empty groups are dropped.
 *
 * Items missing `.owner` are treated as the caller's personal — same defensive
 * fallback as `OwnerBadge`.
 */
export function groupByOwner<T extends OwnedItem>(
  items: T[],
  callerUserId: number,
  myOrgs: Org[],
): OwnerGroup<T>[] {
  const buckets = new Map<string, T[]>();
  const labels = new Map<string, string>();

  for (const item of items) {
    const k = ownerKey(item.owner, callerUserId);
    let arr = buckets.get(k);
    if (!arr) {
      arr = [];
      buckets.set(k, arr);
      labels.set(k, item.owner?.name || item.owner?.slug || '');
    }
    arr.push(item);
  }

  const result: OwnerGroup<T>[] = [];

  const personalKey = `user:${callerUserId}`;
  if (buckets.has(personalKey)) {
    result.push({
      key: personalKey,
      label: '', // signal: use i18n "Personal" string
      ownerType: 'user',
      ownerId: callerUserId,
      items: buckets.get(personalKey)!,
    });
    buckets.delete(personalKey);
  }

  // After personal-key removal, separate user buckets from org buckets:
  const otherUserEntries: Array<[string, T[]]> = [];
  for (const entry of buckets.entries()) {
    if (entry[0].startsWith('user:')) otherUserEntries.push(entry);
  }

  // Band 2: other users, by id ascending
  otherUserEntries.sort(([a], [b]) => Number(a.split(':')[1]) - Number(b.split(':')[1]));
  for (const [k, arr] of otherUserEntries) {
    const idStr = k.split(':')[1];
    result.push({
      key: k,
      label: labels.get(k) || `User #${idStr}`,
      ownerType: 'user',
      ownerId: Number(idStr),
      items: arr,
    });
    buckets.delete(k);
  }

  // Band 3: member orgs in myOrgs order (unchanged)
  for (const org of myOrgs) {
    const k = `org:${org.id}`;
    if (buckets.has(k)) {
      result.push({
        key: k,
        label: org.name,
        ownerType: 'org',
        ownerId: org.id,
        items: buckets.get(k)!,
      });
      buckets.delete(k);
    }
  }

  // Band 4: unknown orgs by id ascending
  const remaining = Array.from(buckets.entries()).sort(
    ([a], [b]) => Number(a.split(':')[1]) - Number(b.split(':')[1]),
  );
  for (const [k, arr] of remaining) {
    const idStr = k.split(':')[1];
    result.push({
      key: k,
      label: labels.get(k) || `Org #${idStr}`,
      ownerType: 'org',
      ownerId: Number(idStr),
      items: arr,
    });
  }

  return result;
}
