import { describe, it, expect } from 'vitest';
import { buildFileTree, type TreeNode } from './changes-file-tree';
import type { DiffFileRow } from './hooks/use-workspace-diff';

const file = (path: string): DiffFileRow => ({
  path,
  status: 'modified',
  additions: 1,
  deletions: 0,
  commentCount: 0,
});

// Compact shape for assertions: dirs as "name/...children", files as "name".
function shape(nodes: TreeNode[]): unknown {
  return nodes.map((n) =>
    n.kind === 'dir' ? { dir: n.name, children: shape(n.children) } : n.name,
  );
}

describe('buildFileTree', () => {
  it('places root-level files (no slash) at the root', () => {
    expect(shape(buildFileTree([file('README.md')]))).toEqual(['README.md']);
  });

  it('nests files under their folders', () => {
    const tree = buildFileTree([file('src/a.ts'), file('src/b.ts')]);
    expect(shape(tree)).toEqual([{ dir: 'src', children: ['a.ts', 'b.ts'] }]);
  });

  it('compresses single-child folder chains', () => {
    const tree = buildFileTree([file('a/b/c/deep.ts')]);
    expect(shape(tree)).toEqual([{ dir: 'a/b/c', children: ['deep.ts'] }]);
  });

  it('does not over-collapse a folder that branches', () => {
    const tree = buildFileTree([file('a/b/x.ts'), file('a/c/y.ts')]);
    // 'a' has two children → not collapsed; each leaf chain stays as-is.
    expect(shape(tree)).toEqual([
      { dir: 'a', children: [
        { dir: 'b', children: ['x.ts'] },
        { dir: 'c', children: ['y.ts'] },
      ] },
    ]);
  });

  it('sorts folders before files, each alphabetically', () => {
    const tree = buildFileTree([
      file('z.txt'),
      file('a.txt'),
      file('lib/util.ts'),
    ]);
    expect(shape(tree)).toEqual([
      { dir: 'lib', children: ['util.ts'] },
      'a.txt',
      'z.txt',
    ]);
  });

  it('keeps the original file row on leaf nodes', () => {
    const tree = buildFileTree([file('src/a.ts')]);
    const dir = tree[0];
    expect(dir.kind).toBe('dir');
    if (dir.kind === 'dir') {
      const leaf = dir.children[0];
      expect(leaf.kind).toBe('file');
      if (leaf.kind === 'file') expect(leaf.file.path).toBe('src/a.ts');
    }
  });
});
