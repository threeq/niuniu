import { useEffect, useState } from 'react';
import { Folder, File, ChevronRight, ChevronDown } from 'lucide-react';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { TreeItem } from '@/types/api';
import { cn } from '@/lib/utils';
import { useWorkspacePanelStore, contentTargetForPath } from '@/stores/workspace-panel-store';

interface FileTreePanelProps {
  workspaceId: string;
}

interface TreeNodeProps {
  item: TreeItem;
  depth: number;
  workspaceId: string;
  selectedPath: string | null;
  onOpenFile: (item: TreeItem) => void;
}

function buildEndpoint(workspaceId: string, path: string): string {
  const base = `/workspaces/${workspaceId}/tree/main`;
  return path ? `${base}?path=${encodeURIComponent(path)}` : base;
}

function TreeNode({ item, depth, workspaceId, selectedPath, onOpenFile }: TreeNodeProps) {
  const [expanded, setExpanded] = useState(false);
  const selected = item.type === 'file' && item.path === selectedPath;

  const { data: childrenData, isLoading: childrenLoading } = useQuery({
    queryKey: ['tree', workspaceId, 'main', item.path],
    queryFn: async () => {
      const endpoint = buildEndpoint(workspaceId, item.path);
      const res = await api.get<{ path: string; items: TreeItem[] }>(endpoint);
      return res.items ?? [];
    },
    enabled: item.type === 'dir' && expanded,
    staleTime: 30_000,
  });

  const children: TreeItem[] = childrenData ?? [];

  const handleClick = () => {
    if (item.type === 'dir') {
      setExpanded((prev) => !prev);
    } else {
      onOpenFile(item);
    }
  };

  return (
    <div>
      <div
        onClick={handleClick}
        className={cn(
          'flex items-center gap-1 py-0.5 select-none cursor-pointer',
          selected ? 'bg-brand-soft' : 'hover:bg-accent',
        )}
        style={{ paddingLeft: `${8 + depth * 16}px` }}
      >
        {item.type === 'dir' ? (
          <>
            {expanded ? (
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
          className={cn(
            'text-xs truncate',
            selected ? 'font-medium text-brand' : 'text-foreground',
          )}
        >
          {item.name}
        </span>
      </div>

      {item.type === 'dir' && expanded && (
        <div>
          {childrenLoading ? (
            <div
              className="text-xs text-muted-foreground py-0.5"
              style={{ paddingLeft: `${8 + (depth + 1) * 16}px` }}
            >
              Loading…
            </div>
          ) : children.length === 0 ? (
            <div
              className="text-xs text-muted-foreground py-0.5"
              style={{ paddingLeft: `${8 + (depth + 1) * 16}px` }}
            >
              Empty
            </div>
          ) : (
            children.map((child) => (
              <TreeNode
                key={child.path}
                item={child}
                depth={depth + 1}
                workspaceId={workspaceId}
                selectedPath={selectedPath}
                onOpenFile={onOpenFile}
              />
            ))
          )}
        </div>
      )}
    </div>
  );
}

export function FileTreePanel({ workspaceId }: FileTreePanelProps) {
  const [items, setItems] = useState<TreeItem[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const openViewer = useWorkspacePanelStore((s) => s.openContentViewer);
  const viewerTarget = useWorkspacePanelStore((s) => s.contentViewer[workspaceId] ?? null);
  const selectedPath =
    viewerTarget && 'path' in viewerTarget && viewerTarget.kind !== 'diff'
      ? viewerTarget.path
      : null;

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- async data fetch: setLoading/setError synchronously indicate request-in-flight before the promise resolves; this is the correct loading-state pattern
    setLoading(true);
    setError(null);

    api
      .get<{ path: string; items: TreeItem[] }>(buildEndpoint(workspaceId, ''))
      .then((res) => {
        setItems(res.items ?? []);
      })
      .catch((err: unknown) => {
        setError(err instanceof Error ? err.message : 'Failed to load files');
      })
      .finally(() => setLoading(false));
  }, [workspaceId]);

  if (loading) {
    return (
      <div className="flex flex-col h-full bg-card">
        <div className="flex items-center justify-center py-8 text-xs text-muted-foreground">
          Loading…
        </div>
      </div>
    );
  }

  if (error) {
    return (
      <div className="flex flex-col h-full bg-card">
        <div className="px-3 py-4 text-xs text-destructive">{error}</div>
      </div>
    );
  }

  if (items.length === 0) {
    return (
      <div className="flex flex-col h-full bg-card">
        <div className="flex items-center justify-center py-8 text-xs text-muted-foreground">
          Empty workspace
        </div>
      </div>
    );
  }

  return (
    <div className="flex flex-col h-full bg-card">
      <div className="flex-1 overflow-y-auto py-1">
        {items.map((item) => (
          <TreeNode
            key={item.path}
            item={item}
            depth={0}
            workspaceId={workspaceId}
            selectedPath={selectedPath}
            onOpenFile={(f) => openViewer(workspaceId, contentTargetForPath(f.path, f.name))}
          />
        ))}
      </div>
    </div>
  );
}
