import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Folder, File, ChevronRight, ChevronDown } from 'lucide-react';
import { useQueries, useQuery } from '@tanstack/react-query';
import { useVirtualizer } from '@tanstack/react-virtual';
import { api } from '@/lib/api';
import type { TreeItem } from '@/types/api';
import { cn } from '@/lib/utils';
import { useWorkspacePanelStore, contentTargetForPath } from '@/stores/workspace-panel-store';

interface FileTreePanelProps {
  workspaceId: string;
}

/** Row height in px — every tree row is a fixed single line, so this is exact
 *  and the list needs no measurement pass. */
const ROW_HEIGHT = 22;

/** Below this many visible rows, mount them all (see CodeSurface for the same
 *  reasoning: windowing a screenful of rows is pure overhead). */
const VIRTUALIZE_THRESHOLD = 100;

function buildEndpoint(workspaceId: string, path: string): string {
  const base = `/workspaces/${workspaceId}/tree/main`;
  return path ? `${base}?path=${encodeURIComponent(path)}` : base;
}

function treeQuery(workspaceId: string, path: string) {
  return {
    queryKey: ['tree', workspaceId, 'main', path],
    queryFn: async () => {
      const res = await api.get<{ path: string; items: TreeItem[] }>(
        buildEndpoint(workspaceId, path),
      );
      return res.items ?? [];
    },
    staleTime: 30_000,
  };
}

/** One rendered line of the tree: a real entry, or a placeholder for a
 *  directory that is loading or turned out to be empty. */
type Row =
  | { kind: 'item'; key: string; item: TreeItem; depth: number }
  | { kind: 'note'; key: string; depth: number; text: 'loading' | 'empty' };

/**
 * Flatten the expanded tree into a linear row list.
 *
 * The old panel recursed through `TreeNode` components, each holding its own
 * `useState` + `useQuery`. That is what made large directories expensive: every
 * entry in every expanded directory was a mounted component with a subscription,
 * whether or not it was on screen. Here expansion state and children live in the
 * panel, the tree is flattened once, and the list windows — so a 1000-entry
 * directory costs 1000 array entries and ~30 DOM nodes.
 */
function flatten(
  items: TreeItem[],
  depth: number,
  expanded: Set<string>,
  childrenOf: Map<string, TreeItem[] | undefined>,
  out: Row[],
) {
  for (const item of items) {
    out.push({ kind: 'item', key: item.path, item, depth });
    if (item.type !== 'dir' || !expanded.has(item.path)) continue;
    const kids = childrenOf.get(item.path);
    if (kids === undefined) {
      out.push({ kind: 'note', key: `${item.path}::loading`, depth: depth + 1, text: 'loading' });
    } else if (kids.length === 0) {
      out.push({ kind: 'note', key: `${item.path}::empty`, depth: depth + 1, text: 'empty' });
    } else {
      flatten(kids, depth + 1, expanded, childrenOf, out);
    }
  }
}

export function FileTreePanel({ workspaceId }: FileTreePanelProps) {
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set());
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const openViewer = useWorkspacePanelStore((s) => s.openContentViewer);
  const viewerTarget = useWorkspacePanelStore((s) => s.contentViewer[workspaceId] ?? null);
  const selectedPath =
    viewerTarget && 'path' in viewerTarget && viewerTarget.kind !== 'diff'
      ? viewerTarget.path
      : null;

  // Switching workspaces must not carry the previous tree's expansion state —
  // the paths belong to a different worktree.
  useEffect(() => {
    setExpanded(new Set());
  }, [workspaceId]);

  const root = useQuery(treeQuery(workspaceId, ''));

  // One query per expanded directory, driven by state rather than by a mounted
  // component per node. Children stay cached (and the rows stay stable) when a
  // directory is collapsed and re-expanded.
  const expandedPaths = useMemo(() => [...expanded].sort(), [expanded]);
  const childQueries = useQueries({
    queries: expandedPaths.map((p) => treeQuery(workspaceId, p)),
  });

  const childrenOf = useMemo(() => {
    const m = new Map<string, TreeItem[] | undefined>();
    expandedPaths.forEach((p, i) => m.set(p, childQueries[i]?.data));
    return m;
  }, [expandedPaths, childQueries]);

  const rows = useMemo(() => {
    const out: Row[] = [];
    flatten(root.data ?? [], 0, expanded, childrenOf, out);
    return out;
  }, [root.data, expanded, childrenOf]);

  const toggle = useCallback((path: string) => {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (!next.delete(path)) next.add(path);
      return next;
    });
  }, []);

  const windowed = rows.length > VIRTUALIZE_THRESHOLD;
  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_HEIGHT,
    getItemKey: (i) => rows[i].key,
    overscan: 15,
    enabled: windowed,
  });

  if (root.isLoading) {
    return (
      <div className="flex flex-col h-full bg-card">
        <div className="flex items-center justify-center py-8 text-xs text-muted-foreground">
          Loading…
        </div>
      </div>
    );
  }

  if (root.error) {
    return (
      <div className="flex flex-col h-full bg-card">
        <div className="px-3 py-4 text-xs text-destructive">
          {root.error instanceof Error ? root.error.message : 'Failed to load files'}
        </div>
      </div>
    );
  }

  if (rows.length === 0) {
    return (
      <div className="flex flex-col h-full bg-card">
        <div className="flex items-center justify-center py-8 text-xs text-muted-foreground">
          Empty workspace
        </div>
      </div>
    );
  }

  const renderRow = (row: Row) => {
    if (row.kind === 'note') {
      return (
        <div
          className="text-xs text-muted-foreground"
          style={{ paddingLeft: `${8 + row.depth * 16}px`, lineHeight: `${ROW_HEIGHT}px` }}
        >
          {row.text === 'loading' ? 'Loading…' : 'Empty'}
        </div>
      );
    }
    const { item } = row;
    const selected = item.type === 'file' && item.path === selectedPath;
    const isOpen = expanded.has(item.path);
    return (
      <div
        onClick={() =>
          item.type === 'dir'
            ? toggle(item.path)
            : openViewer(workspaceId, contentTargetForPath(item.path, item.name))
        }
        className={cn(
          'flex items-center gap-1 select-none cursor-pointer',
          selected ? 'bg-brand-soft' : 'hover:bg-accent',
        )}
        style={{ paddingLeft: `${8 + row.depth * 16}px`, height: `${ROW_HEIGHT}px` }}
      >
        {item.type === 'dir' ? (
          <>
            {isOpen ? (
              <ChevronDown className="h-3 w-3 text-muted-foreground shrink-0" />
            ) : (
              <ChevronRight className="h-3 w-3 text-muted-foreground shrink-0" />
            )}
            <Folder className="h-3.5 w-3.5 text-info shrink-0" />
          </>
        ) : (
          <>
            <span className="w-3 shrink-0" />
            <File className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
          </>
        )}
        <span
          className={cn('text-xs truncate', selected ? 'font-medium text-brand' : 'text-foreground')}
        >
          {item.name}
        </span>
      </div>
    );
  };

  return (
    <div className="flex flex-col h-full bg-card">
      <div ref={scrollRef} className="flex-1 overflow-y-auto py-1">
        {windowed ? (
          <div style={{ height: virtualizer.getTotalSize(), position: 'relative' }}>
            {virtualizer.getVirtualItems().map((vi) => (
              <div
                key={vi.key}
                className="absolute left-0 top-0 w-full"
                style={{ height: vi.size, transform: `translateY(${vi.start}px)` }}
              >
                {renderRow(rows[vi.index])}
              </div>
            ))}
          </div>
        ) : (
          rows.map((row) => <div key={row.key}>{renderRow(row)}</div>)
        )}
      </div>
    </div>
  );
}
