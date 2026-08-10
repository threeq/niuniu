import React, { useRef, useState, useLayoutEffect } from 'react';
import { useVirtualizer } from '@tanstack/react-virtual';
import { useNavigate } from '@tanstack/react-router';
import { ChevronDown, ChevronRight, FolderKanban, SquareArrowOutUpRight } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { cn } from '@/lib/utils';
import { WorkspaceCard } from './workspace-sidebar-card';
import { getProjectColorStyles } from '@/lib/project-color';
import { buildWorkspaceTree } from './workspace-tree';
import type { Workspace, Project } from '@/types/api';

// Above this many flattened rows the list windows (only visible rows mount) to
// keep the DOM bounded at hundreds/thousands of workspaces. Below it, and in any
// environment where the scroll container has no measured height (jsdom tests,
// pre-layout first paint), every row renders — identical output to the
// non-virtualized path, so small sidebars and unit tests are never windowed.
const VIRTUALIZE_THRESHOLD = 60;

type Row =
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
  scrollRef: React.RefObject<HTMLDivElement | null>;
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
// Exported for unit testing the flattening (the highest-risk pure logic).
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

export function WorkspaceVirtualList(props: WorkspaceVirtualListProps) {
  const { t } = useTranslation('workspaces');
  const navigate = useNavigate();
  const listRef = useRef<HTMLDivElement | null>(null);
  const rows = buildRows(props);

  const isSelected = (id: string) => props.selectedIds?.has(Number(id)) ?? false;
  const isActive = (id: string) => String(props.activeWorkspaceId) === String(id);

  const renderRow = (row: Row) => {
    if (row.kind === 'header') {
      const pcs = getProjectColorStyles(row.project.color);
      // id:-1 is the "orphan group" sentinel (project hidden/deleted) — no real
      // kanban to open, so the jump button is only rendered for real projects.
      const canOpenKanban = row.project.id >= 0;
      return (
        <div className="group flex items-center justify-between w-full py-2 px-1 rounded-md hover:bg-accent transition-colors">
          <button
            type="button"
            onClick={() => props.onToggleProjectExpand(row.projKey)}
            className="flex items-center gap-1.5 min-w-0 flex-1 text-left"
          >
            {row.expanded ? (
              <ChevronDown className="w-3.5 h-3.5 text-muted-foreground shrink-0" />
            ) : (
              <ChevronRight className="w-3.5 h-3.5 text-muted-foreground shrink-0" />
            )}
            <FolderKanban className={cn('w-3.5 h-3.5 shrink-0', row.expanded ? pcs.text : 'text-muted-foreground')} aria-hidden />
            <span className={cn('text-xs font-semibold truncate', row.expanded ? pcs.text : 'text-muted-foreground')}>
              {row.project.name}
            </span>
          </button>
          <span className="flex items-center gap-1 shrink-0">
            {canOpenKanban && (
              <button
                type="button"
                onClick={() => navigate({ to: '/projects/$id', params: { id: String(row.project.id) } })}
                aria-label={t('sidebar.openKanban')}
                title={t('sidebar.openKanban')}
                className="p-0.5 rounded text-muted-foreground opacity-0 group-hover:opacity-100 focus-visible:opacity-100 hover:bg-warm-border hover:text-foreground transition-colors"
              >
                <SquareArrowOutUpRight className="w-3.5 h-3.5" />
              </button>
            )}
            <span className="text-[11px] text-muted-foreground font-mono tabular-nums">{row.count}</span>
          </span>
        </div>
      );
    }
    // card row
    if (row.depth === 0) {
      return (
        <div className="flex items-start">
          {row.hasChildren ? (
            <button
              type="button"
              onClick={() => props.onToggleParentCollapse(row.parentId)}
              aria-expanded={!row.collapsed}
              aria-label={t(row.collapsed ? 'sidebar.tree.expand' : 'sidebar.tree.collapse')}
              className="shrink-0 mt-2 p-0.5 rounded text-muted-foreground hover:bg-accent hover:text-foreground transition-colors"
            >
              {row.collapsed ? <ChevronRight className="w-3 h-3" /> : <ChevronDown className="w-3 h-3" />}
            </button>
          ) : (
            <span className="w-4 shrink-0" aria-hidden />
          )}
          <div className="flex-1 min-w-0">
            <WorkspaceCard
              ws={row.ws}
              project={row.project}
              isActive={isActive(String(row.ws.id))}
              selectMode={props.selectMode}
              selected={isSelected(String(row.ws.id))}
              onToggleSelect={props.onToggleSelect}
            />
          </div>
        </div>
      );
    }
    // child card row (depth 1) with tree connector
    return (
      <div className="ml-4 flex items-stretch">
        <div className="relative w-5 shrink-0" aria-hidden>
          <span className={cn('absolute top-0 left-2 w-px bg-warm-border', row.isLast ? 'h-1/2' : 'h-full')} />
          <span className="absolute top-1/2 left-2 right-0 h-px bg-warm-border" />
        </div>
        <div className="flex-1 min-w-0">
          <WorkspaceCard
            ws={row.ws}
            project={row.project}
            isActive={isActive(String(row.ws.id))}
            selectMode={props.selectMode}
            selected={isSelected(String(row.ws.id))}
            onToggleSelect={props.onToggleSelect}
          />
        </div>
      </div>
    );
  };

  // Track the scroll container's height in state (a ref alone can't drive this,
  // since populating scrollRef.current doesn't re-render). ResizeObserver keeps
  // it fresh on layout changes; guarded so jsdom (no ResizeObserver, height 0)
  // simply leaves it at 0 and falls back to rendering every row.
  const { scrollRef } = props;
  const [viewportH, setViewportH] = useState(0);
  useLayoutEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    setViewportH(el.clientHeight);
    if (typeof ResizeObserver === 'undefined') return;
    const ro = new ResizeObserver(() => setViewportH(el.clientHeight));
    ro.observe(el);
    return () => ro.disconnect();
  }, [scrollRef]);

  // Window only when the scroll container has a real measured height AND the row
  // count is large. Otherwise render every row (identical to the old path) so
  // jsdom tests and small sidebars are unaffected.
  const windowed = viewportH > 0 && rows.length > VIRTUALIZE_THRESHOLD;

  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => props.scrollRef.current,
    estimateSize: () => 48,
    overscan: 10,
    getItemKey: (i) => rows[i].key,
    scrollMargin: listRef.current?.offsetTop ?? 0,
    enabled: windowed,
  });

  if (!windowed) {
    return (
      <div ref={listRef} className="px-3">
        {rows.map((row) => (
          <div key={row.key} className={row.kind === 'header' ? 'py-1 border-b border-warm-border/40' : 'py-0.5'}>
            {renderRow(row)}
          </div>
        ))}
      </div>
    );
  }

  const items = virtualizer.getVirtualItems();
  return (
    <div ref={listRef} className="px-3">
      <div style={{ height: virtualizer.getTotalSize(), position: 'relative', width: '100%' }}>
        {items.map((vi) => {
          const row = rows[vi.index];
          return (
            <div
              key={vi.key}
              data-index={vi.index}
              ref={virtualizer.measureElement}
              style={{
                position: 'absolute',
                top: 0,
                left: 0,
                width: '100%',
                transform: `translateY(${vi.start - virtualizer.options.scrollMargin}px)`,
              }}
              className={row.kind === 'header' ? 'py-1 border-b border-warm-border/40' : 'py-0.5'}
            >
              {renderRow(row)}
            </div>
          );
        })}
      </div>
    </div>
  );
}
