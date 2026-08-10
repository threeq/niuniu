import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { useDroppable } from '@dnd-kit/core';
import {
  SortableContext,
  verticalListSortingStrategy,
} from '@dnd-kit/sortable';
import { api } from '@/lib/api';
import type { Column, Issue, Workspace } from '@/types/api';
import { ChevronLeft, ChevronRight, Plus } from 'lucide-react';
import { IssueCard, IssueCardSkeleton } from './IssueCard';
import { Checkbox } from '@/components/ui/checkbox';
import { isIMEComposing } from '@/lib/ime';
import { cn } from '@/lib/utils';

interface ColumnContainerProps {
  column: Column;
  index: number;
  isFirst: boolean;
  isLast: boolean;
  isMoveDisabled?: boolean;
  onMoveColumn?: (columnId: number, newIndex: number) => void;
  isDragOver?: boolean;
  isDraggingAny?: boolean;
  filteredIssues?: Issue[];
  /** When false (default) sub-issues (issues with a parent Epic) are hidden,
   * so the column shows only regular issues and Epics. */
  showSubIssues?: boolean;
  highlightQuery?: string;
  workspaceByIssueId?: Record<string, Workspace>;
  selectedIds?: Set<number>;
  selectionActive?: boolean;
  onToggleSelect?: (issueId: number, shiftKey: boolean, columnIssues: Issue[]) => void;
  onToggleSelectColumn?: (columnIssues: Issue[], checked: boolean) => void;
  onUpdateColumn?: (id: number, name: string) => void;
  onIssueClick?: (issueId: number) => void;
  onCreateIssueInColumn?: (columnId: number) => void;
}

// Map column name to a workflow status accent color.
// Matches niuniu's default English seed names (Backlog / Spec·Plan / Implement /
// Review / Done). Custom column names fall back to a neutral accent.
// See docs/design-system.md §2.5 — `--col-*` tokens.
function getColumnAccentClass(name: string): string {
  const n = name.toLowerCase().trim();
  if (n.includes('backlog')) return 'bg-col-backlog';
  if (n.includes('spec') || n.includes('plan')) return 'bg-col-spec';
  if (n.includes('impl')) return 'bg-col-impl';
  if (n.includes('review')) return 'bg-col-review';
  if (n.includes('done')) return 'bg-col-done';
  return 'bg-warm-border';
}

function ColumnDropZone({ columnId, children }: { columnId: number; children: React.ReactNode }) {
  const { setNodeRef, isOver } = useDroppable({
    id: `column-drop-${columnId}`,
    data: { type: 'column', columnId },
  });

  return (
    <div
      ref={setNodeRef}
      className={`flex-1 overflow-auto p-2 space-y-2 min-h-[200px] transition-colors ${
        isOver ? 'bg-brand-soft' : ''
      }`}
    >
      {children}
    </div>
  );
}

export function ColumnContainer({
  column,
  index,
  isFirst,
  isLast,
  isMoveDisabled,
  onMoveColumn,
  isDragOver,
  isDraggingAny,
  filteredIssues,
  showSubIssues = false,
  highlightQuery,
  workspaceByIssueId,
  selectedIds,
  selectionActive,
  onToggleSelect,
  onToggleSelectColumn,
  onUpdateColumn,
  onIssueClick,
  onCreateIssueInColumn,
}: ColumnContainerProps) {
  const { t } = useTranslation('projects');
  const [isEditing, setIsEditing] = useState(false);
  const [editName, setEditName] = useState(column.name);

  const { data: issues, isLoading: issuesLoading } = useQuery({
    queryKey: ['issues', column.id],
    queryFn: () => api.get<Issue[]>(`/columns/${column.id}/issues`),
    retry: 1,
  });

  // Sub-issue gate — hide issues attached to a parent Epic unless "全部" is on.
  // (When filteredIssues is supplied it is already sub-issue-filtered upstream;
  // re-applying here is a harmless no-op and keeps both render paths consistent.)
  const hideSub = (arr: Issue[]) =>
    showSubIssues ? arr : arr.filter((i) => i.parent_issue_id == null);

  // All visible-eligible issues in this column (denominator for the count badge
  // and the empty/collapse check) — excludes hidden sub-issues.
  const visibleAll = hideSub(issues ?? []);

  // The issues actually rendered in this column (filtered subset when a search/
  // filter is active, otherwise the full visible column). Selection range +
  // select-all operate over this ordered list.
  const displayIssues = filteredIssues !== undefined ? hideSub(filteredIssues) : visibleAll;
  const selectedInColumn = displayIssues.filter((i) => selectedIds?.has(i.id)).length;
  const allColumnSelected =
    displayIssues.length > 0 && selectedInColumn === displayIssues.length;
  const someColumnSelected = selectedInColumn > 0 && !allColumnSelected;

  const handleSaveEdit = () => {
    if (editName.trim() && editName !== column.name) {
      onUpdateColumn?.(column.id, editName.trim());
    }
    setIsEditing(false);
  };

  const handleCancelEdit = () => {
    setEditName(column.name);
    setIsEditing(false);
  };

  const isTrulyEmpty =
    !issuesLoading && filteredIssues === undefined && visibleAll.length === 0;
  const isCollapsed = isTrulyEmpty && !isDraggingAny && !isDragOver;
  const accentClass = getColumnAccentClass(column.name);

  // Collapsed render: narrow vertical strip; clicking expands via add-issue.
  if (isCollapsed) {
    // Provide a drop zone so dragging onto a collapsed column still works as a fallback.
    // (In normal use, isDraggingAny becomes true on drag start and we re-render expanded.)
    return (
      <ColumnCollapsed
        column={column}
        accentClass={accentClass}
        onCreate={() => onCreateIssueInColumn?.(column.id)}
        addIssueLabel={t('kanban.column.addIssue')}
      />
    );
  }

  return (
    <div
      className={`min-w-[280px] max-w-[360px] flex-1 flex-shrink-0 rounded-lg bg-warm-muted flex flex-col shadow-sm max-h-full overflow-hidden
                  transition-[box-shadow] duration-150 ease-out ${
        isDragOver ? 'shadow-lg ring-2 ring-brand/40' : 'border border-warm-border'
      }`}
    >
      {/* Status accent — 2px top bar */}
      <div className={`h-[2px] flex-shrink-0 ${accentClass}`} aria-hidden="true" />

      {/* Column Header */}
      <div className="group/header p-3 border-b border-warm-border flex items-center gap-1 bg-warm-muted">
        {/* Column Name + Count */}
        <div className="flex items-center gap-2 flex-1 min-w-0">
          {onToggleSelectColumn && displayIssues.length > 0 && (
            <Checkbox
              checked={allColumnSelected ? true : someColumnSelected ? 'indeterminate' : false}
              onCheckedChange={(checked) => onToggleSelectColumn(displayIssues, checked === true)}
              aria-label={t('kanban.bulk.selectAll')}
              className={cn(
                'shrink-0 transition-opacity',
                selectionActive || allColumnSelected || someColumnSelected
                  ? 'opacity-100'
                  : 'opacity-0 group-hover/header:opacity-100'
              )}
            />
          )}
          {isEditing ? (
            <input
              type="text"
              value={editName}
              onChange={(e) => setEditName(e.target.value)}
              onBlur={handleSaveEdit}
              onKeyDown={(e) => {
                if (isIMEComposing(e)) return;
                if (e.key === 'Enter') handleSaveEdit();
                if (e.key === 'Escape') handleCancelEdit();
              }}
              className="flex-1 px-2 py-1 text-sm font-medium border rounded focus:outline-none focus:ring-2 focus:ring-ring"
              autoFocus
            />
          ) : (
            <h3
              className="font-semibold text-sm truncate cursor-pointer text-warm-text"
              onDoubleClick={() => setIsEditing(true)}
            >
              {column.name}
            </h3>
          )}
          <span className="flex-shrink-0 text-[11px] text-warm-text-muted bg-warm-surface border border-warm-border px-1.5 py-0.5 rounded-full tabular-nums">
            {filteredIssues !== undefined ? `${displayIssues.length}/${visibleAll.length}` : visibleAll.length}
          </span>
        </div>

        {/* Action Buttons (visible on hover or keyboard focus within header) */}
        <div className="flex items-center gap-0.5 opacity-0 group-hover/header:opacity-100 focus-within:opacity-100 transition-opacity duration-150">
          {!isFirst && (
            <button
              className="p-1 hover:bg-warm-border/50 rounded transition-colors disabled:opacity-50
                         focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand"
              onClick={() => onMoveColumn?.(column.id, index - 1)}
              disabled={isMoveDisabled}
              aria-label={t('kanban.column.moveLeft')}
              title={t('kanban.column.moveLeft')}
            >
              <ChevronLeft className="w-4 h-4 text-warm-text-muted" aria-hidden="true" />
            </button>
          )}
          {!isLast && (
            <button
              className="p-1 hover:bg-warm-border/50 rounded transition-colors disabled:opacity-50
                         focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand"
              onClick={() => onMoveColumn?.(column.id, index + 1)}
              disabled={isMoveDisabled}
              aria-label={t('kanban.column.moveRight')}
              title={t('kanban.column.moveRight')}
            >
              <ChevronRight className="w-4 h-4 text-warm-text-muted" aria-hidden="true" />
            </button>
          )}
        </div>
      </div>

      {/* Column Content - Sortable Context with Drop Zone */}
      <SortableContext
        items={displayIssues.map((i) => i.id)}
        strategy={verticalListSortingStrategy}
      >
        <ColumnDropZone columnId={column.id}>
          {issuesLoading ? (
            <div className="space-y-2">
              <IssueCardSkeleton />
              <IssueCardSkeleton />
              <IssueCardSkeleton />
            </div>
          ) : displayIssues.length > 0 ? (
            displayIssues.map((issue) => (
              <IssueCard
                key={issue.id}
                issue={issue}
                workspace={workspaceByIssueId?.[String(issue.id)]}
                highlightQuery={highlightQuery}
                onClick={() => onIssueClick?.(issue.id)}
                selected={selectedIds?.has(issue.id)}
                selectionActive={selectionActive}
                onToggleSelect={
                  onToggleSelect
                    ? (shiftKey) => onToggleSelect(issue.id, shiftKey, displayIssues)
                    : undefined
                }
              />
            ))
          ) : filteredIssues !== undefined ? (
            <div className="flex flex-col items-center justify-center py-8 px-2 gap-1 text-center">
              <p className="text-xs text-warm-text-muted">{t('kanban.issueColumn.noMatch')}</p>
              <p className="text-[11px] text-warm-text-muted/70">{t('kanban.issueColumn.tryAdjustFilters')}</p>
            </div>
          ) : (
            <div className="flex items-center justify-center py-6 px-2">
              <p className="text-[11px] text-warm-text-muted/60 select-none">
                {t('kanban.column.dragHereShort')}
              </p>
            </div>
          )}
        </ColumnDropZone>
      </SortableContext>

      {/* Footer: in-column add issue button */}
      {onCreateIssueInColumn && (
        <button
          onClick={() => onCreateIssueInColumn(column.id)}
          className="mx-2 mb-2 px-2 py-1.5 text-xs text-warm-text-muted
                     border border-dashed border-warm-border rounded
                     hover:bg-warm-surface hover:text-warm-text hover:border-warm-text-muted/50
                     transition-colors duration-150 flex items-center gap-1 justify-center
                     focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand
                     focus-visible:bg-warm-surface focus-visible:text-warm-text"
        >
          <Plus className="w-3.5 h-3.5" aria-hidden="true" />
          {t('kanban.column.addIssue')}
        </button>
      )}
    </div>
  );
}

// Collapsed (empty + not-dragging) column: narrow vertical strip with name,
// count (always 0 when collapsed) and a quick "+" trigger. Click anywhere to
// create the first issue in this column.
function ColumnCollapsed({
  column,
  accentClass,
  onCreate,
  addIssueLabel,
}: {
  column: Column;
  accentClass: string;
  onCreate: () => void;
  addIssueLabel: string;
}) {
  const { setNodeRef, isOver } = useDroppable({
    id: `column-drop-${column.id}`,
    data: { type: 'column', columnId: column.id },
  });

  return (
    <div
      ref={setNodeRef}
      className={`w-12 flex-shrink-0 rounded-lg bg-warm-muted border border-warm-border flex flex-col shadow-sm overflow-hidden
                  hover:border-warm-text-muted/40 transition-[box-shadow,border-color] duration-150 ease-out
                  ${isOver ? 'ring-2 ring-brand/40 shadow-lg' : ''}`}
      title={`${column.name}  ·  ${addIssueLabel}`}
    >
      <div className={`h-[2px] flex-shrink-0 ${accentClass}`} aria-hidden="true" />
      <button
        onClick={onCreate}
        aria-label={`${column.name}: ${addIssueLabel}`}
        className="flex-1 flex flex-col items-center justify-between py-3 gap-2 group
                   focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand focus-visible:ring-inset"
      >
        <span
          className="text-[11px] text-warm-text-muted [writing-mode:vertical-rl] tracking-wider truncate max-h-[140px]"
        >
          {column.name}
        </span>
        <Plus className="w-3.5 h-3.5 text-warm-text-muted/60 group-hover:text-warm-text transition-colors" aria-hidden="true" />
      </button>
    </div>
  );
}
