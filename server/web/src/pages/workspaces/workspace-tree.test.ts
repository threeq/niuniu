import { describe, it, expect } from 'vitest';
import type { Workspace } from '@/types/api';
import { buildWorkspaceTree, buildDescendantMap } from './workspace-tree';

// Minimal Workspace fixture — only the fields the tree logic reads.
function ws(id: number, opts: { issueId?: number; parentIssueId?: number | null } = {}): Workspace {
  return {
    id: String(id),
    issue_id: opts.issueId != null ? String(opts.issueId) : null,
    parent_issue_id: opts.parentIssueId ?? null,
    name: `ws-${id}`,
    status: 'created',
  } as unknown as Workspace;
}

describe('buildWorkspaceTree', () => {
  it('nests children under the parent workspace resolved via issue id', () => {
    // Parent ws#5 links issue 100; children ws#6/#7 have parent_issue_id 100.
    const list = [
      ws(5, { issueId: 100 }),
      ws(6, { issueId: 101, parentIssueId: 100 }),
      ws(7, { issueId: 102, parentIssueId: 100 }),
    ];
    const tree = buildWorkspaceTree(list);
    expect(tree).toHaveLength(1);
    expect(tree[0].ws.id).toBe('5');
    expect(tree[0].children.map((c) => c.id)).toEqual(['6', '7']);
  });

  it('flattens grandchildren onto the top-most present ancestor (2 levels)', () => {
    const list = [
      ws(1, { issueId: 100 }),
      ws(2, { issueId: 101, parentIssueId: 100 }),
      ws(3, { issueId: 102, parentIssueId: 101 }), // grandchild
    ];
    const tree = buildWorkspaceTree(list);
    expect(tree).toHaveLength(1);
    expect(tree[0].ws.id).toBe('1');
    expect(tree[0].children.map((c) => c.id).sort()).toEqual(['2', '3']);
  });

  it('treats a child whose parent workspace is absent as a top-level root', () => {
    // parent issue 100 has no workspace in the list.
    const list = [ws(6, { issueId: 101, parentIssueId: 100 })];
    const tree = buildWorkspaceTree(list);
    expect(tree).toHaveLength(1);
    expect(tree[0].ws.id).toBe('6');
    expect(tree[0].children).toHaveLength(0);
  });

  it('preserves input order for roots and children', () => {
    const list = [
      ws(30, { issueId: 300 }),
      ws(10, { issueId: 100 }),
      ws(12, { issueId: 120, parentIssueId: 100 }),
      ws(11, { issueId: 110, parentIssueId: 100 }),
    ];
    const tree = buildWorkspaceTree(list);
    expect(tree.map((n) => n.ws.id)).toEqual(['30', '10']);
    expect(tree[1].children.map((c) => c.id)).toEqual(['12', '11']);
  });

  it('does not loop on a self-referential parent chain', () => {
    // Defensive: an issue somehow pointing at itself must not hang.
    const list = [ws(1, { issueId: 100, parentIssueId: 100 })];
    const tree = buildWorkspaceTree(list);
    expect(tree).toHaveLength(1);
    expect(tree[0].ws.id).toBe('1');
  });
});

describe('buildDescendantMap', () => {
  it('maps a parent ws id to its direct child ws ids', () => {
    const list = [
      ws(5, { issueId: 100 }),
      ws(6, { issueId: 101, parentIssueId: 100 }),
      ws(7, { issueId: 102, parentIssueId: 100 }),
      ws(8, { issueId: 200 }), // childless — absent from map
    ];
    const map = buildDescendantMap(list);
    expect(map.get('5')).toEqual(['6', '7']);
    expect(map.has('8')).toBe(false);
  });
});
