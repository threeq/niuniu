import { useState, useCallback, useMemo, useRef, useContext } from 'react';
import { createPortal } from 'react-dom';
import { useQueryClient, useMutation, useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import {
  DndContext,
  DragOverlay,
  closestCorners,
  pointerWithin,
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
  type DragStartEvent,
  type DragEndEvent,
  type DragOverEvent,
  type CollisionDetection,
} from '@dnd-kit/core';
import {
  sortableKeyboardCoordinates,
} from '@dnd-kit/sortable';
import { useColumns } from '@/lib/hooks/use-columns';
import { ColumnContainer } from './ColumnContainer';
import { SearchFilter, type FilterState } from './search-filter';
import { BulkActionsToolbar } from './bulk-actions-toolbar';
import { LoadingSkeleton } from '@/components/shared/loading-skeleton';
import { EmptyState } from '@/components/shared/empty-state';
import { Plus, Columns3, ListTodo } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { api, labels as labelsApi } from '@/lib/api';
import { mergeWorkspaceGitStatus, sidebarGitPath } from '@/lib/hooks/use-workspaces';
import type { Project, Issue, Workspace, WorkspaceStatus, WorkspaceGitStatus } from '@/types/api';
import { KanbanIssueDetailPanel } from './KanbanIssueDetailPanel';
import { KanbanHeaderSlotContext } from './kanban-header-slot';
import { CreateColumnDialog } from '@/components/dialogs/create-column-dialog';
import { IssueQuickCreateDialog } from './issue-quick-create-dialog';
import { WORKSPACE_STATUS_LABELS } from '@/lib/workspace-status';
// Custom collision detection: prioritize column drop zones (pointerWithin) so empty
// columns are reliably detected, then fall back to closestCorners for card-level sorting.
const kanbanCollisionDetection: CollisionDetection = (args) => {
  // First check pointerWithin — finds column drop zones the pointer is physically inside
  const pointerCollisions = pointerWithin(args);
  const columnHit = pointerCollisions.find(c => String(c.id).startsWith('column-drop-'));
  if (columnHit) {
    // Also check closestCorners for card-level precision
    const cornerCollisions = closestCorners(args);
    const cardHit = cornerCollisions.find(c => !String(c.id).startsWith('column-drop-'));
    if (cardHit) {
      // Only use the card if it belongs to the same column the pointer is in,
      // otherwise we'd move the issue to the wrong column
      const targetColId = String(columnHit.id).replace('column-drop-', '');
      const cardInSameColumn = pointerCollisions.some(c => c.id === cardHit.id);
      if (cardInSameColumn) return [cardHit];
      // Card is in a different column — check if any pointerWithin card matches
      const localCard = pointerCollisions.find(
        c => !String(c.id).startsWith('column-drop-') && String(c.id) !== targetColId
      );
      if (localCard) return [localCard];
    }
    return [columnHit];
  }
  return closestCorners(args);
};

interface KanbanBoardProps {
  project: Project;
  filteredIssuesByColumn?: Record<string, Issue[]>;
  highlightQuery?: string;
  onIssueClick?: (issueId: string) => void;
}

export function KanbanBoard({ project, filteredIssuesByColumn: filteredIssuesByColumnProp, highlightQuery: highlightQueryProp, onIssueClick }: KanbanBoardProps) {
  const { t } = useTranslation('projects');
  const queryClient = useQueryClient();
  // When the project detail page provides a header slot, the toolbar is portaled
  // into the tab row (single merged 40px header). Otherwise it renders inline.
  const headerSlot = useContext(KanbanHeaderSlotContext);
  const [activeIssue, setActiveIssue] = useState<Issue | null>(null);
  const [overId, setOverId] = useState<string | null>(null);
  const [selectedIssueId, setSelectedIssueId] = useState<number | null>(null);
  const [quickCreateColumnId, setQuickCreateColumnId] = useState<number | null>(null);
  const dragOverColumnRef = useRef<number | null>(null);

  // --- Search / filter state (lives here so it can read the board-level
  // all-issues query and feed filteredIssuesByColumn down to the columns) ---
  const [filterState, setFilterState] = useState<FilterState | null>(null);
  const [filteredFlat, setFilteredFlat] = useState<Issue[] | null>(null);
  const handleFilterChange = useCallback((filtered: Issue[], state: FilterState) => {
    setFilteredFlat(filtered);
    setFilterState(state);
  }, []);

  // --- Multi-select (bulk ops) state ---
  const [selectedIds, setSelectedIds] = useState<Set<number>>(new Set());
  const lastSelectedRef = useRef<number | null>(null);
  const selectionActive = selectedIds.size > 0;

  const toggleSelect = useCallback(
    (issueId: number, shiftKey: boolean, columnIssues: Issue[]) => {
      setSelectedIds((prev) => {
        const next = new Set(prev);
        if (shiftKey && lastSelectedRef.current != null) {
          const ids = columnIssues.map((i) => i.id);
          const a = ids.indexOf(lastSelectedRef.current);
          const b = ids.indexOf(issueId);
          if (a !== -1 && b !== -1) {
            const [lo, hi] = a < b ? [a, b] : [b, a];
            for (let k = lo; k <= hi; k++) next.add(ids[k]);
            return next;
          }
        }
        if (next.has(issueId)) next.delete(issueId);
        else next.add(issueId);
        return next;
      });
      // Anchor for the next shift-range; set outside the updater so it stays a
      // pure state reducer (React may invoke the updater twice in dev/StrictMode).
      lastSelectedRef.current = issueId;
    },
    []
  );

  const toggleSelectColumn = useCallback((columnIssues: Issue[], checked: boolean) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      for (const i of columnIssues) {
        if (checked) next.add(i.id);
        else next.delete(i.id);
      }
      return next;
    });
  }, []);

  const clearSelection = useCallback(() => {
    setSelectedIds(new Set());
    lastSelectedRef.current = null;
  }, []);

  const [creatingColumn, setCreatingColumn] = useState(false);

  const {
    columns,
    isLoading,
    error,
    updateColumn,
    moveColumn,
    isMovingColumn,
  } = useColumns(project.id);

  // Fetch all issues for stats count + search/filter source of truth
  const { data: allIssues } = useQuery({
    queryKey: ['all-issues', project.id],
    queryFn: () => api.get<Issue[]>(`/projects/${project.id}/issues`),
    retry: 1,
  });

  // Project labels — drive the label filter dimension + the bulk label editor.
  const { data: projectLabels } = useQuery({
    queryKey: ['project-labels', project.id],
    queryFn: () => labelsApi.list(project.id),
    retry: 1,
  });

  // Fetch all workspaces and build issueId -> workspace map. The list is git-less
  // (方案 B); git badges are merged from the lazy sidebar-git endpoint below so
  // the issue cards' changes/ahead counts stay populated.
  const { data: rawWorkspaces } = useQuery({
    queryKey: ['workspaces'],
    queryFn: () => api.get<Workspace[]>('/workspaces'),
    retry: 1,
  });
  const { data: workspaceGitStatus } = useQuery({
    // Key intentionally outside the ['workspaces'] prefix so frequent bg_task
    // invalidations don't retrigger the O(N) git recompute (see use-workspaces).
    queryKey: ['sidebar-git', null],
    queryFn: () => api.get<WorkspaceGitStatus[]>(sidebarGitPath(null)),
    enabled: !!rawWorkspaces,
    retry: 1,
  });
  const allWorkspaces = useMemo(
    () => mergeWorkspaceGitStatus(rawWorkspaces, workspaceGitStatus),
    [rawWorkspaces, workspaceGitStatus],
  );

  const workspaceByIssueId = useMemo(() => {
    const map: Record<string, Workspace> = {};
    if (allWorkspaces) {
      for (const ws of allWorkspaces) {
        if (ws.issue_id) {
          map[ws.issue_id] = ws;
        }
      }
    }
    return map;
  }, [allWorkspaces]);

  // Compute stats
  const stats = useMemo(() => {
    if (!allIssues) return { total: 0, byColumn: {} as Record<string, number> };
    const byColumn: Record<string, number> = {};
    allIssues.forEach(issue => {
      byColumn[issue.column_id] = (byColumn[issue.column_id] || 0) + 1;
    });
    return { total: allIssues.length, byColumn };
  }, [allIssues]);

  // Whether any filter dimension is active. When inactive we pass `undefined`
  // down so each column keeps rendering its own per-column query (the board's
  // all-issues snapshot can lag a per-column optimistic update).
  const hasActiveFilter = !!filterState && (
    filterState.searchQuery !== '' ||
    filterState.priority !== '' ||
    filterState.assignee !== '' ||
    filterState.labelIds.length > 0
  );

  // Group the filtered flat list by column. Every column gets an entry (even
  // empty) so a column with zero matches shows the "no match" state instead of
  // falling back to its full list.
  const computedFilteredByColumn = useMemo(() => {
    if (!hasActiveFilter || !filteredFlat) return undefined;
    const map: Record<string, Issue[]> = {};
    (columns ?? []).forEach((col) => {
      map[col.id] = [];
    });
    filteredFlat.forEach((iss) => {
      (map[iss.column_id] ??= []).push(iss);
    });
    Object.keys(map).forEach((k) => {
      map[k].sort((a, b) => a.position - b.position);
    });
    return map;
  }, [hasActiveFilter, filteredFlat, columns]);

  const filteredIssuesByColumn = filteredIssuesByColumnProp ?? computedFilteredByColumn;
  const highlightQuery = highlightQueryProp ?? (hasActiveFilter ? filterState?.searchQuery : undefined);

  // Sub-issues (issues attached to a parent Epic) are hidden by default; the
  // SearchFilter "全部" toggle flips this. Applied per-column so the default
  // (no-filter) path keeps using each column's own optimistic-update query.
  const showSubIssues = filterState?.showSubIssues ?? false;

  const handleIssueClick = useCallback((issueId: number) => {
    setSelectedIssueId(issueId);
    onIssueClick?.(String(issueId));
  }, [onIssueClick]);

  const handleCloseDetail = useCallback(() => {
    setSelectedIssueId(null);
  }, []);

  const sensors = useSensors(
    useSensor(PointerSensor, {
      activationConstraint: { distance: 8 },
    }),
    useSensor(KeyboardSensor, {
      coordinateGetter: sortableKeyboardCoordinates,
    })
  );

  const moveIssueMutation = useMutation({
    mutationFn: ({ issueId, data }: { issueId: number; sourceIssue: Issue; data: { column_id: number; position: number } }) => {
      return api.put<Issue>(`/issues/${issueId}/move`, data);
    },
    onMutate: async ({ issueId, sourceIssue, data: moveData }) => {
      await queryClient.cancelQueries({ queryKey: ['issues'] });
      const previousIssuesMap: Record<string, Issue[]> = {};
      const queries = queryClient.getQueriesData<Issue[]>({ queryKey: ['issues'] });
      queries.forEach(([queryKey, issuesArr]) => {
        if (issuesArr !== undefined) {
          previousIssuesMap[JSON.stringify(queryKey)] = issuesArr;
        }
      });

      const sourceKey = ['issues', sourceIssue.column_id] as const;
      const targetKey = ['issues', moveData.column_id] as const;

      // Update source column: remove the issue
      const sourceIssues = queryClient.getQueryData<Issue[]>(sourceKey);
      if (sourceIssues) {
        const updated = sourceIssues.filter(i => i.id !== issueId).map((issue, idx) => ({ ...issue, position: idx }));
        queryClient.setQueryData<Issue[]>(sourceKey, updated);
      }

      // Update target column: insert the issue at new position
      // Use ?? [] so the card appears immediately even if the target column's cache is empty
      const targetIssues = queryClient.getQueryData<Issue[]>(targetKey) ?? [];
      const updated = [...targetIssues];
      const movedIssue = { ...sourceIssue, position: moveData.position };
      const insertIndex = Math.min(moveData.position, updated.length);
      updated.splice(insertIndex, 0, movedIssue);
      const reindexed = updated.map((issue, idx) => ({ ...issue, position: idx }));
      queryClient.setQueryData<Issue[]>(targetKey, reindexed);

      return { previousIssuesMap };
    },
    onError: (_err, _variables, context) => {
      if (context?.previousIssuesMap) {
        Object.entries(context?.previousIssuesMap).forEach(([queryKey, issuesArr]) => {
          queryClient.setQueryData<Issue[]>(JSON.parse(queryKey), issuesArr);
        });
      }
      queryClient.invalidateQueries({ queryKey: ['issues'] });
      queryClient.invalidateQueries({ queryKey: ['all-issues'] });
      // Keep activeIssue to show the animation (issue will be restored by cache)
    },
    onSuccess: () => {
      // Invalidate both individual column queries AND the combined all-issues query
      queryClient.invalidateQueries({ queryKey: ['issues'] });
      queryClient.invalidateQueries({ queryKey: ['all-issues'] });
      // Clear activeIssue to hide DragOverlay immediately on success
      setActiveIssue(null);
    },
  });

  const handleDragStart = useCallback((event: DragStartEvent) => {
    if (!columns) return;
    for (const column of columns) {
      const issues = queryClient.getQueryData<Issue[]>(['issues', column.id]);
      const issue = issues?.find(i => i.id === event.active.id);
      if (issue) {
        setActiveIssue(issue);
        return;
      }
    }
  }, [columns, queryClient]);

  const handleDragOver = useCallback((event: DragOverEvent) => {
    const { over } = event;
    setOverId(over ? String(over.id) : null);

    // Track which column the pointer is over for cross-column fallback
    if (!over || !columns) {
      dragOverColumnRef.current = null;
      return;
    }
    const oid = String(over.id);
    if (oid.startsWith('column-drop-')) {
      dragOverColumnRef.current = Number(oid.replace('column-drop-', ''));
    } else {
      for (const col of columns) {
        const issues = queryClient.getQueryData<Issue[]>(['issues', col.id]);
        if (issues?.some(i => i.id === Number(oid))) {
          dragOverColumnRef.current = Number(col.id);
          return;
        }
      }
    }
  }, [columns, queryClient]);

  const handleDragEnd = useCallback((event: DragEndEvent) => {
    const { active, over } = event;

    // --- Issue drag ---
    const savedDragOverColumnId = dragOverColumnRef.current;
    setOverId(null);
    dragOverColumnRef.current = null;
    if (!over || !columns) {
      setActiveIssue(null);
      return;
    }
    // Don't clear activeIssue here - let it persist until mutation resolves
    // This keeps the DragOverlay visible during the API call

    const activeId = Number(active.id);
    const overIdNum = Number(over.id);
    const overId = String(over.id);

    // Dropped on itself — no-op (explicit NaN check for column-drop-* ids)
    if (!isNaN(overIdNum) && activeId === overIdNum) {
      setActiveIssue(null);
      return;
    }

    // Find source issue and column
    let sourceIssue: Issue | undefined;
    let sourceColumnId: number | undefined;
    for (const col of columns) {
      const issues = queryClient.getQueryData<Issue[]>(['issues', col.id]);
      const issue = issues?.find(i => i.id === activeId);
      if (issue) {
        sourceIssue = issue;
        sourceColumnId = Number(col.id);
        break;
      }
    }
    if (!sourceIssue || !sourceColumnId) return;

    // Same column reorder
    const sourceIssues = queryClient.getQueryData<Issue[]>(['issues', sourceColumnId]);
    const targetIssue = sourceIssues?.find(i => i.id === overIdNum);
    if (targetIssue && targetIssue !== sourceIssue) {
      moveIssueMutation.mutate({
        issueId: activeId,
        sourceIssue,
        data: { column_id: sourceColumnId, position: targetIssue.position },
      });
      return;
    }

    // Cross-column move
    let targetColumnId: number | undefined;
    let targetPosition = 0;

    if (overId.startsWith('column-drop-')) {
      // Dropping on empty column area
      targetColumnId = Number(overId.replace('column-drop-', ''));
      const targetIssues = queryClient.getQueryData<Issue[]>(['issues', targetColumnId]);
      targetPosition = targetIssues?.length || 0;
    } else {
      // Dropping on an issue in another column
      for (const col of columns) {
        if (Number(col.id) === sourceColumnId) continue;
        const issues = queryClient.getQueryData<Issue[]>(['issues', col.id]);
        const issue = issues?.find(i => i.id === overIdNum);
        if (issue) {
          targetColumnId = Number(col.id);
          targetPosition = issue.position;
          break;
        }
      }
    }

    // Fallback: if no target found but we tracked a different column during dragOver,
    // use that column (fixes adjacent column drag where closestCorners detects the
    // dragged card itself as the over target)
    if (!targetColumnId && savedDragOverColumnId && savedDragOverColumnId !== sourceColumnId) {
      targetColumnId = savedDragOverColumnId;
      const targetIssues = queryClient.getQueryData<Issue[]>(['issues', targetColumnId]);
      targetPosition = targetIssues?.length || 0;
    }

    if (targetColumnId && targetColumnId !== sourceColumnId) {
      moveIssueMutation.mutate({
        issueId: activeId,
        sourceIssue,
        data: { column_id: targetColumnId, position: targetPosition },
      });
    } else {
      setActiveIssue(null);
    }
  }, [columns, queryClient, moveIssueMutation]);

  const handleMoveColumn = useCallback((columnId: number, newIndex: number) => {
    moveColumn({ columnId, position: newIndex });
  }, [moveColumn]);

  const handleAddIssue = () => {
    if (!columns || columns.length === 0) return;
    setQuickCreateColumnId(columns[0].id);
  };

  const handleCreateIssueInColumn = useCallback((columnId: number) => {
    setQuickCreateColumnId(columnId);
  }, []);

  if (isLoading) {
    return <KanbanBoardSkeleton />;
  }

  if (error) {
    return (
      <div className="flex items-center justify-center h-full">
        <EmptyState
          title={t('kanban.loadFailed')}
          description={t('kanban.loadFailedDescription')}
          icon={<Columns3 className="h-12 w-12 text-muted-foreground" />}
          action={
            <Button onClick={() => window.location.reload()}>
              {t('common:actions.retry')}
            </Button>
          }
        />
      </div>
    );
  }

  if (!columns || columns.length === 0) {
    return (
      <div className="flex items-center justify-center h-full">
        <EmptyState
          title={t('kanban.noColumns')}
          description={t('kanban.noColumnsDescription')}
          icon={<Columns3 className="h-12 w-12 text-muted-foreground" />}
          action={
            <Button onClick={() => setCreatingColumn(true)}>
              <Plus className="w-4 h-4 mr-2" />
              {t('tabs.settings.columns.addColumn')}
            </Button>
          }
        />
        <CreateColumnDialog
          open={creatingColumn}
          projectId={project.id}
          onOpenChange={setCreatingColumn}
        />
      </div>
    );
  }

  // Compute which column is being dragged over (for visual highlight)
  const visualDragOverColumnId = (() => {
    if (!overId || !columns) return null;
    if (overId.startsWith('column-drop-')) {
      return Number(overId.replace('column-drop-', ''));
    }
    for (const col of columns) {
      const issues = queryClient.getQueryData<Issue[]>(['issues', col.id]);
      if (issues?.some(i => i.id === Number(overId))) {
        return Number(col.id);
      }
    }
    return null;
  })();

  // Toolbar body — project info + stats chips + search/filter + add. Rendered
  // either portaled into the tab row (merged single header) or inline as its own
  // row, depending on whether the detail page supplied a header slot.
  const toolbarInner = (
    <>
      {/* Project name + stats — stats compressed into two compact chips so the
          long "共 N 个问题 · 共 N 个栏目" sentence no longer competes with the
          search/filter row. */}
      <div className="flex items-center gap-2 min-w-0 shrink-0">
        <div className="flex items-center gap-2 min-w-0">
          <ListTodo className="w-4 h-4 text-warm-text-muted shrink-0" />
          <span className="text-sm font-medium text-warm-text truncate">{project.name}</span>
        </div>
        <div className="hidden md:flex items-center gap-1.5">
          <span
            className="inline-flex items-center gap-1 rounded-md bg-warm-muted px-2 py-0.5 text-xs text-warm-text-muted"
            title={`${t('kanban.header.totalIssuesPrefix')}${stats.total}${t('kanban.header.totalIssuesSuffix')}`}
          >
            <span className="font-semibold text-warm-text tabular-nums">{stats.total}</span>
            {t('kanban.header.totalIssuesSuffix')}
          </span>
          <span
            className="inline-flex items-center gap-1 rounded-md bg-warm-muted px-2 py-0.5 text-xs text-warm-text-muted"
            title={`${t('kanban.header.totalColumnsPrefix')}${columns?.length ?? 0}${t('kanban.header.totalColumnsSuffix')}`}
          >
            <span className="font-semibold text-warm-text tabular-nums">{columns?.length ?? 0}</span>
            {t('kanban.header.totalColumnsSuffix')}
          </span>
        </div>
      </div>

      {/* Search / filter — grows to fill the remaining width */}
      <div className="flex-1 min-w-[200px]">
        <SearchFilter
          issues={allIssues ?? []}
          labels={projectLabels ?? []}
          onFilterChange={handleFilterChange}
        />
      </div>

      {/* Add issue */}
      <Button
        variant="outline"
        size="sm"
        className="h-8 shrink-0"
        onClick={handleAddIssue}
      >
        <Plus className="w-3.5 h-3.5 mr-1" aria-hidden="true" />
        {t('kanban.header.addIssue')}
      </Button>
    </>
  );

  return (
    <DndContext
      sensors={sensors}
      collisionDetection={kanbanCollisionDetection}
      onDragStart={handleDragStart}
      onDragOver={handleDragOver}
      onDragEnd={handleDragEnd}
    >
      <div className="flex h-full">
        <div className="flex-1 flex flex-col min-w-0">
          {headerSlot ? (
            // Merged into the tab row: right-aligned controls with a divider
            // separating them from the 看板/记忆/设置 tabs.
            createPortal(
              <div className="flex flex-1 items-center gap-x-3 min-w-0 pl-1">
                <span className="mx-1 h-4 w-px bg-warm-border shrink-0" aria-hidden="true" />
                {toolbarInner}
              </div>,
              headerSlot,
            )
          ) : (
            // Standalone: toolbar on its own compact 40px row.
            <div className="flex min-h-10 items-center gap-x-4 gap-y-2 flex-wrap px-4 py-1.5 border-b border-warm-border bg-warm-surface">
              {toolbarInner}
            </div>
          )}

          {/* Kanban columns */}
          <div className="flex-1 overflow-x-auto overflow-y-hidden bg-warm-canvas">
            <div className="flex gap-4 h-full p-4 min-w-full">
              {columns.map((column, index) => (
                <ColumnContainer
                  key={column.id}
                  column={column}
                  index={index}
                  isFirst={index === 0}
                  isLast={index === columns.length - 1}
                  isDragOver={visualDragOverColumnId === Number(column.id)}
                  isDraggingAny={activeIssue !== null}
                  isMoveDisabled={isMovingColumn}
                  filteredIssues={filteredIssuesByColumn?.[column.id]}
                  showSubIssues={showSubIssues}
                  highlightQuery={highlightQuery}
                  workspaceByIssueId={workspaceByIssueId}
                  selectedIds={selectedIds}
                  selectionActive={selectionActive}
                  onToggleSelect={toggleSelect}
                  onToggleSelectColumn={toggleSelectColumn}
                  onUpdateColumn={(id, name) => updateColumn({ id, data: { name } })}
                  onMoveColumn={handleMoveColumn}
                  onIssueClick={handleIssueClick}
                  onCreateIssueInColumn={handleCreateIssueInColumn}
                />
              ))}
            </div>
          </div>

          {/* Bulk actions toolbar — visible only while issues are selected */}
          {selectionActive && (
            <BulkActionsToolbar
              selectedIds={Array.from(selectedIds)}
              selectedIssues={(allIssues ?? []).filter((i) => selectedIds.has(i.id))}
              columns={columns}
              labels={projectLabels ?? []}
              onClear={clearSelection}
              onDone={clearSelection}
            />
          )}
        </div>

        {/* Issue Detail Panel */}
        {selectedIssueId !== null && (
          <KanbanIssueDetailPanel
            issueId={String(selectedIssueId)}
            columns={columns}
            onClose={handleCloseDetail}
            onOpenIssue={(id) => setSelectedIssueId(id)}
          />
        )}
      </div>
      {quickCreateColumnId != null && (
        <IssueQuickCreateDialog
          open={quickCreateColumnId != null}
          onOpenChange={(o) => { if (!o) setQuickCreateColumnId(null); }}
          projectId={project.id}
          columnId={quickCreateColumnId}
          issues={allIssues ?? []}
          onCreated={(id) => { setQuickCreateColumnId(null); setSelectedIssueId(id); }}
        />
      )}
      <DragOverlay>
        {activeIssue ? (() => {
          const dragWs = workspaceByIssueId[String(activeIssue.id)];
          return (
            <div className="bg-warm-surface border border-warm-border rounded-lg p-3 shadow-lg">
              <div className="font-medium text-sm text-warm-text">{activeIssue.title}</div>
              {activeIssue.description && activeIssue.description.trim() && (
                <div className="text-xs text-warm-text-muted mt-1 line-clamp-2">
                  {activeIssue.description}
                </div>
              )}
              {dragWs && (
                <div className="mt-2 flex items-center gap-1.5 text-[11px] px-2 py-1 rounded border bg-warm-muted text-warm-text-muted border-warm-border">
                  <span className="truncate">{dragWs.name}</span>
                  <span className="flex-shrink-0 ml-auto">{WORKSPACE_STATUS_LABELS[(dragWs.status as WorkspaceStatus)] ?? dragWs.status}</span>
                </div>
              )}
            </div>
          );
        })() : null}
      </DragOverlay>
    </DndContext>
  );
}

function KanbanBoardSkeleton() {
  return (
    <div className="h-full overflow-x-auto overflow-y-hidden bg-warm-canvas">
      <div className="flex gap-4 h-full p-4 min-w-full">
        {[1, 2, 3].map((i) => (
          <div
            key={i}
            className="min-w-[280px] max-w-[360px] flex-1 flex-shrink-0 border border-warm-border rounded-lg bg-warm-muted flex flex-col shadow-sm"
          >
            <div className="h-[2px] bg-warm-border" aria-hidden="true" />
            <div className="p-3 border-b border-warm-border">
              <LoadingSkeleton variant="text" className="h-5 w-24" />
            </div>
            <div className="flex-1 p-2 space-y-2">
              <LoadingSkeleton variant="text" count={3} className="h-20" />
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}
