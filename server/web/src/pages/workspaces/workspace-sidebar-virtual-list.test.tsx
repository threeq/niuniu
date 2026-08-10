import { describe, it, expect } from 'vitest';
import { buildRows, type WorkspaceVirtualListProps } from './workspace-sidebar-virtual-list';
import type { Workspace, Project } from '@/types/api';

// Minimal workspace factory — buildRows only reads id / issue_id /
// parent_issue_id (for the tree) plus whatever WorkspaceCard needs downstream.
function ws(id: number, issueId: number, parentIssueId?: number): Workspace {
  return {
    id: String(id),
    issue_id: String(issueId),
    parent_issue_id: parentIssueId != null ? String(parentIssueId) : undefined,
  } as unknown as Workspace;
}

function baseProps(over: Partial<WorkspaceVirtualListProps>): WorkspaceVirtualListProps {
  return {
    scrollRef: { current: null },
    orderedProjectKeys: [],
    byProject: new Map(),
    getProject: (k) => ({ id: 1, name: k } as Project),
    isProjectExpanded: () => true,
    onToggleProjectExpand: () => {},
    isParentCollapsed: () => false,
    onToggleParentCollapse: () => {},
    ...over,
  };
}

// Compact summary of a row list for readable assertions:
//   H:<projKey>            -> project header
//   C<depth>:<wsId>[*]     -> card at depth, * = last child
function summarize(rows: ReturnType<typeof buildRows>): string[] {
  return rows.map((r) =>
    r.kind === 'header' ? `H:${r.projKey}` : `C${r.depth}:${r.ws.id}${r.isLast ? '*' : ''}`,
  );
}

describe('buildRows', () => {
  it('flattens an expanded project with a parent + two children, and a collapsed project', () => {
    const p1 = [ws(1, 1), ws(2, 2, 1), ws(3, 3, 1)]; // A root, B/C children of A
    const p2 = [ws(4, 4)];
    const rows = buildRows(baseProps({
      orderedProjectKeys: ['p1', 'p2'],
      byProject: new Map([['p1', p1], ['p2', p2]]),
      isProjectExpanded: (k) => k === 'p1', // p2 collapsed
    }));

    expect(summarize(rows)).toEqual([
      'H:p1',
      'C0:1', // parent A
      'C1:2', // child B
      'C1:3*', // child C (last)
      'H:p2', // collapsed -> no cards
    ]);
    // Parent row carries hasChildren so the collapse chevron renders.
    const parent = rows.find((r) => r.kind === 'card' && r.ws.id === '1');
    expect(parent && parent.kind === 'card' && parent.hasChildren).toBe(true);
  });

  it('omits children when the parent is collapsed', () => {
    const p1 = [ws(1, 1), ws(2, 2, 1)];
    const rows = buildRows(baseProps({
      orderedProjectKeys: ['p1'],
      byProject: new Map([['p1', p1]]),
      isParentCollapsed: (id) => id === '1', // collapse A
    }));

    expect(summarize(rows)).toEqual(['H:p1', 'C0:1']); // child B hidden
    const parent = rows.find((r) => r.kind === 'card');
    expect(parent && parent.kind === 'card' && parent.collapsed).toBe(true);
  });

  it('emits only headers when all projects are collapsed', () => {
    const rows = buildRows(baseProps({
      orderedProjectKeys: ['p1', 'p2'],
      byProject: new Map([['p1', [ws(1, 1)]], ['p2', [ws(2, 2)]]]),
      isProjectExpanded: () => false,
    }));
    expect(summarize(rows)).toEqual(['H:p1', 'H:p2']);
  });
});
