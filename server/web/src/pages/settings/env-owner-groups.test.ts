import { describe, expect, it } from 'vitest'
import { combineOwnerGroups, ownerKeyOf } from './env-owner-groups'
import type { EnvAccount, EnvProvider } from '@/types/api'

const userOwner = (id: number, name?: string) =>
  ({ type: 'user', id, ...(name ? { name } : {}) }) as const
const orgOwner = (id: number, name?: string) =>
  ({ type: 'org', id, ...(name ? { name } : {}) }) as const

function provider(id: number, owner?: EnvProvider['owner']): EnvProvider {
  return {
    id,
    name: `p${id}`,
    platform: '',
    description: '',
    base_urls: {},
    api_key: '',
    model: '',
    haiku_model: '',
    sonnet_model: '',
    opus_model: '',
    subagent_model: '',
    extra_env: {},
    group_name: '',
    group_position: 0,
    enabled: true,
    cooldown_until: '',
    owner,
    created_at: '',
    updated_at: '',
  }
}

function account(id: number, owner?: EnvAccount['owner']): EnvAccount {
  return {
    id,
    name: `a${id}`,
    platform: '',
    description: '',
    api_key: '',
    owner,
    created_at: '',
    updated_at: '',
  }
}

describe('ownerKeyOf', () => {
  it('folds the seeded system owner (user #0) into the personal user in personal edition', () => {
    expect(ownerKeyOf({ owner: userOwner(0) }, true, 7)).toBe('user:7')
  })

  it('keeps the system owner distinct in team edition', () => {
    expect(ownerKeyOf({ owner: userOwner(0) }, false, 7)).toBe('user:0')
  })

  it('keys org and missing owners', () => {
    expect(ownerKeyOf({ owner: orgOwner(3) }, false, 7)).toBe('org:3')
    expect(ownerKeyOf({}, false, 7)).toBe('none')
  })
})

describe('combineOwnerGroups', () => {
  it('puts one owner’s providers and accounts in the same block', () => {
    const groups = combineOwnerGroups(
      [provider(1, userOwner(7))],
      [account(1, userOwner(7))],
      false,
      7,
    )
    expect(groups).toHaveLength(1)
    expect(groups[0].key).toBe('user:7')
    expect(groups[0].providers.map((p) => p.id)).toEqual([1])
    expect(groups[0].accounts.map((a) => a.id)).toEqual([1])
  })

  it('merges seeded system rows into the personal block in personal edition', () => {
    const groups = combineOwnerGroups(
      [provider(1, userOwner(0)), provider(2, userOwner(7))],
      [account(1, userOwner(0))],
      true,
      7,
    )
    expect(groups).toHaveLength(1)
    expect(groups[0].key).toBe('user:7')
    expect(groups[0].providers.map((p) => p.id)).toEqual([1, 2])
    expect(groups[0].accounts.map((a) => a.id)).toEqual([1])
  })

  it('keeps seeded system rows as their own block in team edition', () => {
    const groups = combineOwnerGroups(
      [provider(1, userOwner(0)), provider(2, userOwner(7))],
      [],
      false,
      7,
    )
    // Same rank, no names: id order makes the order deterministic, so the
    // seeded system block (id 0) leads the personal block (id 7).
    expect(groups.map((g) => g.key)).toEqual(['user:0', 'user:7'])
  })

  it('orders personal first, orgs by name, then unowned', () => {
    const groups = combineOwnerGroups(
      [provider(1, orgOwner(9, 'Zeta')), provider(2, orgOwner(8, 'Alpha')), provider(3)],
      [account(1, userOwner(7))],
      false,
      7,
    )
    expect(groups.map((g) => g.key)).toEqual(['user:7', 'org:8', 'org:9', 'none'])
  })

  it('keeps an owner that has only accounts (no providers)', () => {
    const groups = combineOwnerGroups([], [account(1, orgOwner(3, 'Acme'))], false, 7)
    expect(groups).toHaveLength(1)
    expect(groups[0].providers).toEqual([])
    expect(groups[0].accounts.map((a) => a.id)).toEqual([1])
  })
})
