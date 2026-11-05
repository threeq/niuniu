// Pure owner-grouping rules for the subscription-platform settings list.
//
// Kept out of the component because one rule is subtle and worth testing:
// system-seeded rows carry owner_type='user', owner_id=0. In the single-user
// personal edition those rows belong to the one real user, so they must fold
// into that user's group instead of surfacing a phantom "用户 #0".

import type { EnvAccount, EnvProvider } from '@/types/api'

export type OwnerInfo = { type: string; id: number; name?: string; slug?: string }
export type OwnedItem = { owner?: OwnerInfo }

export type ItemGroup<T> = {
  key: string
  owner?: OwnerInfo
  items: T[]
}

// One owner's block: every provider AND platform account that owner holds.
export type OwnerGroup = {
  key: string
  owner?: OwnerInfo
  providers: EnvProvider[]
  accounts: EnvAccount[]
}

export function isSystemOwner(owner?: OwnerInfo): boolean {
  return !!owner && owner.type === 'user' && owner.id === 0
}

export function ownerKeyOf(item: OwnedItem, personalMode: boolean, currentUserId: number): string {
  const owner = item.owner
  if (!owner) return 'none'
  if (personalMode && isSystemOwner(owner)) return `user:${currentUserId}`
  return `${owner.type}:${owner.id}`
}

export function compareOwnerGroups(a: { owner?: OwnerInfo }, b: { owner?: OwnerInfo }): number {
  const rank = (owner?: OwnerInfo) => (owner ? (owner.type === 'user' ? 0 : 1) : 2)
  const byRank = rank(a.owner) - rank(b.owner)
  if (byRank !== 0) return byRank
  const byName = (a.owner?.name ?? '').localeCompare(b.owner?.name ?? '')
  if (byName !== 0) return byName
  // Deterministic tiebreaker for same-named owners (and the unnamed system
  // owner, id 0).
  return (a.owner?.id ?? 0) - (b.owner?.id ?? 0)
}

export function groupByOwner<T extends OwnedItem>(
  items: T[],
  personalMode: boolean,
  currentUserId: number,
): ItemGroup<T>[] {
  const groups = new Map<string, ItemGroup<T>>()
  for (const item of items) {
    const key = ownerKeyOf(item, personalMode, currentUserId)
    if (!groups.has(key)) groups.set(key, { key, owner: item.owner, items: [] })
    groups.get(key)!.items.push(item)
  }
  return [...groups.values()].sort(compareOwnerGroups)
}

// combineOwnerGroups merges the provider and account lists into one block per
// owner, so a provider and the account its API key references sit together.
export function combineOwnerGroups(
  providers: EnvProvider[],
  accounts: EnvAccount[],
  personalMode: boolean,
  currentUserId: number,
): OwnerGroup[] {
  const groups = new Map<string, OwnerGroup>()
  for (const g of groupByOwner(providers, personalMode, currentUserId)) {
    groups.set(g.key, { key: g.key, owner: g.owner, providers: g.items, accounts: [] })
  }
  for (const g of groupByOwner(accounts, personalMode, currentUserId)) {
    const existing = groups.get(g.key)
    if (existing) {
      existing.accounts = g.items
      if (!existing.owner) existing.owner = g.owner
    } else {
      groups.set(g.key, { key: g.key, owner: g.owner, providers: [], accounts: g.items })
    }
  }
  return [...groups.values()].sort(compareOwnerGroups)
}
