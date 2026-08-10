import type { DiffFileRow } from '@/lib/hooks/use-workspace-diff';

/** A node in the changed-files folder tree. */
export type TreeNode =
  | { kind: 'dir'; name: string; path: string; children: TreeNode[] }
  | { kind: 'file'; name: string; file: DiffFileRow };

/**
 * Builds a nested folder tree from a flat list of changed files. Single-child
 * folder chains are compressed (`a/b/c` → one "a/b/c" node) so deep paths (e.g.
 * Java packages) don't nest dozens of near-empty levels. Each level is sorted
 * folders-first, then alphabetically.
 */
export function buildFileTree(files: DiffFileRow[]): TreeNode[] {
  const rootChildren: TreeNode[] = [];
  const dirChildren = new Map<string, TreeNode[]>([['', rootChildren]]);

  const ensureDir = (path: string): TreeNode[] => {
    const existing = dirChildren.get(path);
    if (existing) return existing;
    const idx = path.lastIndexOf('/');
    const parentPath = idx < 0 ? '' : path.slice(0, idx);
    const name = idx < 0 ? path : path.slice(idx + 1);
    const children: TreeNode[] = [];
    ensureDir(parentPath).push({ kind: 'dir', name, path, children });
    dirChildren.set(path, children);
    return children;
  };

  for (const file of files) {
    const idx = file.path.lastIndexOf('/');
    const dirPath = idx < 0 ? '' : file.path.slice(0, idx);
    const name = idx < 0 ? file.path : file.path.slice(idx + 1);
    ensureDir(dirPath).push({ kind: 'file', name, file });
  }

  // Compress single-child folder chains (a/b/c → "a/b/c").
  const collapse = (nodes: TreeNode[]): TreeNode[] =>
    nodes.map((node) => {
      if (node.kind !== 'dir') return node;
      let { name, path, children } = node;
      while (children.length === 1) {
        const child = children[0];
        if (child.kind !== 'dir') break;
        name = `${name}/${child.name}`;
        path = child.path;
        children = child.children;
      }
      return { kind: 'dir', name, path, children: collapse(children) };
    });

  const sort = (nodes: TreeNode[]) => {
    nodes.sort((a, b) =>
      a.kind !== b.kind ? (a.kind === 'dir' ? -1 : 1) : a.name.localeCompare(b.name),
    );
    for (const n of nodes) if (n.kind === 'dir') sort(n.children);
  };

  const result = collapse(rootChildren);
  sort(result);
  return result;
}
