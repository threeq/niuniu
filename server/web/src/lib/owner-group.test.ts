import { describe, it, expect } from 'vitest';
import { groupByOwner } from './owner-group';

interface Item { id: number; owner?: { type: string; id: number; name?: string; slug?: string } }

const personal: Item = { id: 1, owner: { type: 'user', id: 7 } };
const orgA1: Item = { id: 2, owner: { type: 'org', id: 10, name: 'OrgA' } };
const orgA2: Item = { id: 3, owner: { type: 'org', id: 10, name: 'OrgA' } };
const orgB1: Item = { id: 4, owner: { type: 'org', id: 20, name: 'OrgB' } };
const stranger: Item = { id: 5, owner: { type: 'user', id: 99 } };
const myOrgs = [
  { id: 10, slug: 'a', name: 'OrgA', description: '', created_at: '', updated_at: '' },
  { id: 20, slug: 'b', name: 'OrgB', description: '', created_at: '', updated_at: '' },
];

describe('groupByOwner', () => {
  it('orders Personal first, then orgs in myOrgs order', () => {
    const groups = groupByOwner([orgB1, orgA1, personal, orgA2], 7, myOrgs);
    expect(groups.map((g) => g.key)).toEqual(['user:7', 'org:10', 'org:20']);
    expect(groups[1].items.map((i) => i.id)).toEqual([2, 3]); // OrgA preserves input order
  });

  it('drops empty groups', () => {
    const groups = groupByOwner([personal], 7, myOrgs);
    expect(groups.map((g) => g.key)).toEqual(['user:7']);
  });

  it('orders Personal → other users (by id) → member orgs → unknown orgs', () => {
    const orgUnknown: Item = { id: 6, owner: { type: 'org', id: 99, name: 'Unk' } };
    const groups = groupByOwner([stranger, orgUnknown, personal], 7, myOrgs);
    expect(groups.map((g) => g.key)).toEqual(['user:7', 'user:99', 'org:99']);
  });

  it('uses callerUserId as Personal group key, not zero', () => {
    const groups = groupByOwner([personal], 7, myOrgs);
    expect(groups[0].key).toBe('user:7');
  });

  it('treats item without owner as personal of caller (defensive)', () => {
    const orphan: Item = { id: 99 };
    const groups = groupByOwner([orphan], 7, myOrgs);
    expect(groups[0].key).toBe('user:7');
    expect(groups[0].items).toEqual([orphan]);
  });

  it('returns label resolved from myOrgs / owner.name, falling back to id', () => {
    const groups = groupByOwner([personal, orgA1], 7, myOrgs);
    expect(groups[0].label).toBe(''); // Personal: caller decides — empty signals "use i18n key"
    expect(groups[1].label).toBe('OrgA');
  });
});
