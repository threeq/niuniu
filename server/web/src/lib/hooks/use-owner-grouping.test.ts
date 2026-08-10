import { describe, it, expect } from 'vitest';
import { computeOwnerGrouping } from './use-owner-grouping';
import type { OwnerRef } from '@/types/org';

const u = (id: number, name = 'User'): OwnerRef => ({ type: 'user', id, name });
const o = (id: number, name: string): OwnerRef => ({ type: 'org', id, name });

describe('computeOwnerGrouping', () => {
  it('returns flat when user has no orgs', () => {
    const items = [{ owner: u(1) }, { owner: u(1) }];
    const r = computeOwnerGrouping(items, /*currentUserId*/ 1, /*myOrgs*/ []);
    expect(r.mode).toBe('flat');
  });

  it('returns flat when user has 1 org and all items are in it', () => {
    const orgs = [{ id: 5, name: 'Acme' }];
    const items = [{ owner: o(5, 'Acme') }, { owner: o(5, 'Acme') }];
    const r = computeOwnerGrouping(items, 1, orgs);
    expect(r.mode).toBe('flat');
  });

  it('groups when user has personal + 1 org with mixed items', () => {
    const orgs = [{ id: 5, name: 'Acme' }];
    const items = [{ owner: u(1) }, { owner: o(5, 'Acme') }];
    const r = computeOwnerGrouping(items, 1, orgs);
    expect(r.mode).toBe('grouped');
    if (r.mode !== 'grouped') return;
    expect(r.groups[0].label).toBe('Personal');
    expect(r.groups[0].icon).toBe('user');
    expect(r.groups[1].label).toBe('Acme');
  });

  it('orders orgs alphabetically; Personal always first', () => {
    const orgs = [{ id: 5, name: 'Bravo' }, { id: 6, name: 'Alpha' }];
    const items = [
      { owner: u(1) },
      { owner: o(5, 'Bravo') },
      { owner: o(6, 'Alpha') },
    ];
    const r = computeOwnerGrouping(items, 1, orgs);
    if (r.mode !== 'grouped') return expect.fail('expected grouped');
    expect(r.groups.map(g => g.label)).toEqual(['Personal', 'Alpha', 'Bravo']);
  });
});
