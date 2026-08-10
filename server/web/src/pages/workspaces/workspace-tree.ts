import type { Workspace } from '@/types/api';

// Sidebar parent/child hierarchy. A workspace is a CHILD when its linked issue
// carries a `parent_issue_id` AND another workspace in the same list is linked
// to that parent issue (workspace ↔ issue is 1:1, so we resolve the parent
// workspace via its `issue_id`). The tree mirrors the issue hierarchy exactly.
//
// We flatten to TWO levels on purpose: the sidebar is narrow (200–480px), so
// deep indentation would crush the dense cards. Grandchildren are re-parented
// onto their top-most present ancestor instead of nesting further.

export interface WorkspaceTreeNode {
  ws: Workspace;
  children: Workspace[];
}

function parentIssueKey(ws: Workspace): string | null {
  return ws.parent_issue_id != null ? String(ws.parent_issue_id) : null;
}

export function buildWorkspaceTree(workspaces: Workspace[]): WorkspaceTreeNode[] {
  // issue_id -> workspace (only issue-linked workspaces can be parents).
  const byIssue = new Map<string, Workspace>();
  for (const ws of workspaces) {
    if (ws.issue_id != null) byIssue.set(String(ws.issue_id), ws);
  }

  // A workspace is a child only when its DIRECT parent workspace is present.
  // If the parent issue has no workspace here, the child falls back to a root
  // (still marked with the ↳ sub-issue glyph on its card).
  const isChild = (ws: Workspace): boolean => {
    const pk = parentIssueKey(ws);
    if (pk == null) return false;
    const parent = byIssue.get(pk);
    return parent != null && parent.id !== ws.id;
  };

  // Walk up to the top-most present ancestor (flatten to 2 levels). A visited
  // set guards against accidental cycles in the parent chain.
  const topAncestor = (ws: Workspace): Workspace => {
    let cur = ws;
    const seen = new Set<string>([String(cur.id)]);
    for (;;) {
      const pk = parentIssueKey(cur);
      if (pk == null) return cur;
      const parent = byIssue.get(pk);
      if (!parent || parent.id === cur.id || seen.has(String(parent.id))) return cur;
      seen.add(String(parent.id));
      cur = parent;
    }
  };

  const nodes: WorkspaceTreeNode[] = [];
  const nodeByWsId = new Map<string, WorkspaceTreeNode>();

  // Roots first, preserving the incoming order.
  for (const ws of workspaces) {
    if (!isChild(ws)) {
      const node: WorkspaceTreeNode = { ws, children: [] };
      nodes.push(node);
      nodeByWsId.set(String(ws.id), node);
    }
  }
  // Attach children to their top-most ancestor root (order preserved).
  for (const ws of workspaces) {
    if (!isChild(ws)) continue;
    const root = topAncestor(ws);
    const node = nodeByWsId.get(String(root.id));
    if (node) {
      node.children.push(ws);
    } else {
      // Ancestor root missing (defensive) — surface the child as its own root.
      const orphan: WorkspaceTreeNode = { ws, children: [] };
      nodes.push(orphan);
      nodeByWsId.set(String(ws.id), orphan);
    }
  }
  return nodes;
}

// workspace id -> flattened direct-child workspace ids. Used for cascade
// selection: selecting a parent in batch mode also (de)selects its children.
export function buildDescendantMap(workspaces: Workspace[]): Map<string, string[]> {
  const map = new Map<string, string[]>();
  for (const node of buildWorkspaceTree(workspaces)) {
    if (node.children.length > 0) {
      map.set(
        String(node.ws.id),
        node.children.map((c) => String(c.id)),
      );
    }
  }
  return map;
}
