// Pure row-flattening for the workspace sidebar. Lives outside the component
// module so `workspace-sidebar-virtual-list.tsx` exports only components (the
// React Fast Refresh constraint), and so the highest-risk pure logic stays
// directly unit-testable.
import type { RefObject } from 'react';

import { buildWorkspaceTree } from './workspace-tree';
import type { Workspace, Project } from '@/types/api';

export type Row =
  | { kind: 'header'; key: string; projKey: string; project: Project; count: number; expanded: boolean }
  | {
      kind: 'card';
      key: string;
      ws: Workspace;
      project: Project;
      depth: 0 | 1;
      hasChildren: boolean;
      collapsed: boolean;
      isLast: boolean;
      parentId: string;
    };

export interface WorkspaceVirtualListProps {
  /** The shared scroll container (also holds the priority zone above the list). */
  scrollRef: RefObject<HTMLDivElement | null>;
  orderedProjectKeys: string[];
  byProject: Map<string, Workspace[]>;
  /** Resolves a Project (or a fallback shape) for a project key + its workspaces. */
  getProject: (projKey: string, wss: Workspace[]) => Project;
  activeWorkspaceId?: string;
  selectMode?: boolean;
  selectedIds?: Set<number>;
  onToggleSelect?: (id: string) => void;
  isProjectExpanded: (projKey: string) => boolean;
  onToggleProjectExpand: (projKey: string) => void;
  isParentCollapsed: (parentId: string) => boolean;
  onToggleParentCollapse: (parentId: string) => void;
}

// Flatten the grouped/collapsible/2-level-tree sidebar into a linear row list so
// it can be windowed. Respects project expand state and per-parent collapse.
export function buildRows(props: WorkspaceVirtualListProps): Row[] {
  const rows: Row[] = [];
  for (const projKey of props.orderedProjectKeys) {
    const wss = props.byProject.get(projKey) ?? [];
    const project = props.getProject(projKey, wss);
    const expanded = props.isProjectExpanded(projKey);
    rows.push({ kind: 'header', key: `h:${projKey}`, projKey, project, count: wss.length, expanded });
    if (!expanded) continue;
    for (const node of buildWorkspaceTree(wss)) {
      const parentId = String(node.ws.id);
      const hasChildren = node.children.length > 0;
      const collapsed = hasChildren && props.isParentCollapsed(parentId);
      rows.push({
        kind: 'card', key: `c:${parentId}`, ws: node.ws, project, depth: 0,
        hasChildren, collapsed, isLast: false, parentId,
      });
      if (hasChildren && !collapsed) {
        node.children.forEach((child, i) => {
          rows.push({
            kind: 'card', key: `c:${child.id}`, ws: child, project, depth: 1,
            hasChildren: false, collapsed: false, isLast: i === node.children.length - 1,
            parentId,
          });
        });
      }
    }
  }
  return rows;
}
