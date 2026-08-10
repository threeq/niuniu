import { useSortable } from '@dnd-kit/sortable';
import { CSS } from '@dnd-kit/utilities';
import { useNavigate } from '@tanstack/react-router';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import type { Issue, Workspace, WorkspaceStatus, EpicProgress } from '@/types/api';
import { epicApi } from '@/lib/api';
import { HighlightText } from './search-filter';
import { Box, Circle, FileEdit, GitMerge, Layers, CornerDownRight } from 'lucide-react';
import { WORKSPACE_STATUS_LABELS } from '@/lib/workspace-status';
import { contrastTextColor } from '@/lib/label-color';
import { avatarColorFor } from '@/lib/avatar-color';
import { ExecStatusBadge } from './exec-status-badge';
import { Checkbox } from '@/components/ui/checkbox';
import { cn } from '@/lib/utils';
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip';
import { ExternalIssueBadge } from '@/components/kanban/ExternalIssueBadge';

interface IssueCardProps {
  issue: Issue;
  workspace?: Workspace;
  isDragging?: boolean;
  onClick?: () => void;
  highlightQuery?: string;
  /** Selection (bulk-ops) — when `onToggleSelect` is provided a checkbox is
   * rendered. `selected` drives the checked + ring-highlight state;
   * `selectionActive` (any card selected board-wide) keeps every checkbox
   * visible so multi-select stays discoverable. */
  selected?: boolean;
  selectionActive?: boolean;
  onToggleSelect?: (shiftKey: boolean) => void;
}

// Workspace status badge colors — migrated to design system semantic tokens.
// One rule per status; opacity modifiers (/10, /30) replace the manual
// dark-mode palette pairing the previous hardcoded Tailwind colors required.
// See docs/design-system.md §2.3 (semantic colors) and §10.1 (anti-patterns).
const wsCardStyles: Record<WorkspaceStatus, { dotColor: string; bgColor: string }> = {
  created:      { dotColor: 'text-warm-text-muted/70', bgColor: 'bg-warm-muted text-warm-text-muted border-warm-border' },
  running:      { dotColor: 'text-success',            bgColor: 'bg-success/10 text-success border-success/30' },
  needs_review: { dotColor: 'text-warning',            bgColor: 'bg-warning/10 text-warning border-warning/30' },
  attention:    { dotColor: 'text-destructive',        bgColor: 'bg-destructive/10 text-destructive border-destructive/30' },
  completed:    { dotColor: 'text-info',               bgColor: 'bg-info/10 text-info border-info/30' },
  deleting:     { dotColor: 'text-warning',            bgColor: 'bg-warning/10 text-warning border-warning/30' },
};

// Design system: priority colors are independent from `success/warning/destructive`
// to avoid green-low collision with the Done column. See docs/design-system.md §2.4.
const priorityColors: Record<number, string> = {
  0: 'bg-prio-low/15 text-prio-low border border-prio-low/30',
  1: 'bg-prio-medium/15 text-prio-medium border border-prio-medium/30',
  2: 'bg-prio-high/15 text-prio-high border border-prio-high/30',
  3: 'bg-prio-critical/15 text-prio-critical border border-prio-critical/30',
};

const priorityBarColors: Record<number, string> = {
  0: 'bg-prio-low',
  1: 'bg-prio-medium',
  2: 'bg-prio-high',
  3: 'bg-prio-critical',
};

export function IssueCard({ issue, workspace, isDragging, onClick, highlightQuery, selected, selectionActive, onToggleSelect }: IssueCardProps) {
  const { t } = useTranslation('projects');
  const navigate = useNavigate();
  const priorityLabels: Record<number, string> = {
    0: t('kanban.priority.low'),
    1: t('kanban.priority.medium'),
    2: t('kanban.priority.high'),
    3: t('kanban.priority.critical'),
  };
  const isEpic = issue.issue_type === 'epic';
  // A sub-issue is any issue attached to a parent Epic. Independent of isEpic:
  // an Epic can itself be a child of another Epic, in which case both markers
  // show. Regular issues (no parent, not an Epic) get neither.
  const isSubIssue = issue.parent_issue_id != null;
  // Cheapest correct source for child progress: a small per-epic-card query to
  // the epic-progress endpoint. Only fires for epic cards, dedup'd by issue id.
  const { data: epicProgress } = useQuery<EpicProgress>({
    queryKey: ['epic-progress', issue.id],
    queryFn: () => epicApi.epicProgress(issue.id),
    enabled: isEpic,
    staleTime: 30_000,
  });
  const {
    attributes,
    listeners,
    setNodeRef,
    transform,
    transition,
    isDragging: isSortableDragging,
  } = useSortable({ id: issue.id });

  const style = {
    transform: CSS.Transform.toString(transform),
    transition,
  };

  const isActuallyDragging = isDragging || isSortableDragging;
  const wsStatus = (workspace?.status ?? 'created') as WorkspaceStatus;
  const wsStyle = workspace ? (wsCardStyles[wsStatus] ?? wsCardStyles.created) : null;

  const prioBar =
    issue.priority !== undefined && issue.priority !== null
      ? priorityBarColors[issue.priority] ?? 'bg-warm-border'
      : null;

  const cardNode = (
    <div
      ref={setNodeRef}
      style={style}
      {...attributes}
      {...listeners}
      onClick={(e) => {
        e.stopPropagation();
        onClick?.();
      }}
      className={cn(
        'relative group bg-warm-surface border border-warm-border rounded-lg shadow-sm flex overflow-hidden',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand',
        selected ? 'ring-2 ring-brand border-brand' : '',
        'cursor-grab hover:shadow-md hover:-translate-y-px transition-[box-shadow,transform] duration-150 ease-out',
        isActuallyDragging ? 'opacity-50 shadow-lg scale-105 rotate-2' : '',
      )}
    >
      {prioBar && <div className={`w-[3px] flex-shrink-0 ${prioBar}`} aria-hidden="true" />}
      {onToggleSelect && (
        <div
          className={cn(
            'absolute top-2 left-2 z-10 transition-opacity duration-150',
            selected || selectionActive ? 'opacity-100' : 'opacity-0 group-hover:opacity-100'
          )}
          onPointerDown={(e) => e.stopPropagation()}
          onClick={(e) => {
            e.stopPropagation();
            onToggleSelect((e as React.MouseEvent).shiftKey);
          }}
        >
          <Checkbox
            checked={selected}
            className="pointer-events-none bg-warm-surface"
            aria-label={t('kanban.bulk.selectIssue', { title: issue.title })}
          />
        </div>
      )}
      <div className={cn('flex-1 min-w-0 p-3', (selected || selectionActive) && 'pl-7')}>
      {/* Title */}
      <div className="font-medium text-sm text-warm-text">
        <span
          className="text-[11px] font-normal text-warm-text-muted mr-1 cursor-pointer hover:text-warm-text tabular-nums"
          onClick={(e) => {
            e.stopPropagation();
            navigator.clipboard.writeText(`#${issue.id}`).catch(() => {});
          }}
          title={t('kanban.card.copyId')}
        >
          #{issue.id}
        </span>
        {highlightQuery ? (
          <HighlightText text={issue.title} query={highlightQuery} />
        ) : (
          issue.title
        )}
      </div>

      {/* Epic badge + child progress */}
      {isEpic && (
        <div className="mt-1.5 flex items-center gap-1.5">
          <span className="inline-flex items-center gap-1 rounded border border-brand/30 bg-brand-soft px-1.5 py-0.5 text-[10px] font-medium text-brand">
            <Layers className="h-3 w-3" aria-hidden="true" />
            {t('kanban.epic.badge')}
          </span>
          {epicProgress && epicProgress.total > 0 && (
            <span
              className="text-[10px] tabular-nums text-warm-text-muted"
              title={t('kanban.epic.progress', {
                done: epicProgress.done,
                total: epicProgress.total,
              })}
            >
              {epicProgress.done}/{epicProgress.total}
            </span>
          )}
          {/* Execution status — only once execution has actually started. */}
          {(() => {
            const st = epicProgress?.exec_status ?? issue.exec_status;
            return st && st !== 'idle' ? <ExecStatusBadge status={st} /> : null;
          })()}
        </div>
      )}

      {/* Sub-issue badge — shown when this issue is attached to a parent Epic.
          Muted (vs the Epic badge's brand color) to read as a secondary,
          subordinate marker. Renders alongside the Epic badge for an Epic that
          is itself a child. */}
      {isSubIssue && (
        <div className="mt-1.5 flex items-center gap-1.5">
          <span className="inline-flex items-center gap-1 rounded border border-warm-border bg-warm-muted px-1.5 py-0.5 text-[10px] font-medium text-warm-text-muted">
            <CornerDownRight className="h-3 w-3" aria-hidden="true" />
            {t('kanban.subIssue.badge')}
          </span>
        </div>
      )}

      {/* Execution status for a standalone (non-epic) issue — once started, and
          especially for the terminal states (blocked / waiting / abandoned), with
          the reason surfaced on hover. Epic cards render their badge above. */}
      {!isEpic && issue.exec_status && issue.exec_status !== 'idle' && (
        <div className="mt-1.5 flex items-center gap-1.5">
          {issue.exec_status_reason ? (
            <TooltipProvider>
              <Tooltip>
                <TooltipTrigger asChild>
                  <span><ExecStatusBadge status={issue.exec_status} /></span>
                </TooltipTrigger>
                <TooltipContent>{issue.exec_status_reason}</TooltipContent>
              </Tooltip>
            </TooltipProvider>
          ) : (
            <ExecStatusBadge status={issue.exec_status} />
          )}
        </div>
      )}

      {/* Description */}
      {issue.description && issue.description.trim() && (
        <div className="text-xs text-warm-text-muted mt-1 line-clamp-2">
          {highlightQuery ? (
            <HighlightText text={issue.description} query={highlightQuery} />
          ) : (
            issue.description
          )}
        </div>
      )}

      {/* Workspace badge */}
      {workspace && wsStyle && (
        <div
          className={`mt-2 flex items-center gap-1.5 text-[11px] px-2 py-1 rounded border cursor-pointer hover:opacity-80 transition-opacity ${wsStyle.bgColor}`}
          onClick={(e) => {
            e.stopPropagation();
            navigate({ to: '/workspaces/$id', params: { id: String(workspace.id) } });
          }}
        >
          <Box className="w-3 h-3 flex-shrink-0" />
          <span className="truncate">{workspace.name}</span>
          {workspace.ahead_count > 0 && (
            <span
              className="flex items-center gap-0.5 flex-shrink-0 tabular-nums"
              title={t('kanban.card.workspace.ahead', { count: workspace.ahead_count })}
            >
              <GitMerge className="w-2.5 h-2.5" />
              <span>{workspace.ahead_count}</span>
            </span>
          )}
          {workspace.changes_count > 0 && (
            <span
              className="flex items-center gap-0.5 flex-shrink-0 tabular-nums"
              title={t('kanban.card.workspace.changes', { count: workspace.changes_count })}
            >
              <FileEdit className="w-2.5 h-2.5" />
              <span>{workspace.changes_count}</span>
            </span>
          )}
          <Circle className={`w-1.5 h-1.5 flex-shrink-0 fill-current ${wsStyle.dotColor} ml-auto`} />
          <span className="flex-shrink-0">{WORKSPACE_STATUS_LABELS[wsStatus]}</span>
        </div>
      )}

      {/* Labels + Assignee avatars row */}
      <div className="flex items-center justify-between mt-1.5 pt-1.5 border-t border-warm-border/60">
        <div className="flex flex-wrap gap-1">
          {(issue.labels ?? []).slice(0, 3).map((l) => (
            <span
              key={l.id}
              className="rounded px-1.5 py-0.5 text-[11px] leading-tight"
              style={{ backgroundColor: l.color, color: contrastTextColor(l.color) }}
            >
              {l.name}
            </span>
          ))}
          {(issue.labels?.length ?? 0) > 3 && (
            <span className="bg-warm-muted text-warm-text-muted rounded px-1.5 py-0.5 text-[11px]">
              +{issue.labels!.length - 3}
            </span>
          )}
        </div>
        <div className="flex -space-x-1">
          {(issue.assignees ?? []).slice(0, 3).map((a) => (
            <div
              key={a.id}
              title={a.display_name}
              className="h-5 w-5 rounded-full text-white text-[10px] font-medium flex items-center justify-center ring-2 ring-warm-surface"
              style={{ backgroundColor: avatarColorFor(a.display_name) }}
            >
              {a.display_name[0]?.toUpperCase()}
            </div>
          ))}
          {(issue.assignees?.length ?? 0) > 3 && (
            <div className="h-5 w-5 rounded-full bg-warm-muted text-warm-text-muted text-[10px] font-medium flex items-center justify-center ring-2 ring-warm-surface">
              +{issue.assignees!.length - 3}
            </div>
          )}
        </div>
      </div>

      {/* Footer: middle indicators + priority */}
      <div className="mt-2 flex items-center justify-between">
        {/* Middle indicators */}
        <div className="flex items-center gap-2 flex-1">
          {issue.due_date && (
            <span className={`text-[11px] tabular-nums ${
              new Date(issue.due_date) < new Date() ? 'text-destructive' : 'text-warm-text-muted'
            }`}>
              📅 {issue.due_date}
            </span>
          )}
          {issue.checklist_stats && issue.checklist_stats.total > 0 && (
            <span className="text-[11px] text-warm-text-muted tabular-nums">
              ☑ {issue.checklist_stats.completed}/{issue.checklist_stats.total}
            </span>
          )}
        </div>
        <div className="flex items-center gap-2">
          {issue.external_source && issue.external_source !== '' && (
            <ExternalIssueBadge
              provider={issue.external_source}
              number={issue.external_number}
              externalId={issue.external_id}
              url={issue.external_url}
            />
          )}
          {issue.priority !== undefined && issue.priority !== null && (
            <span
              className={`text-[10px] px-1.5 py-0.5 rounded ${
                priorityColors[issue.priority] || 'bg-accent text-muted-foreground'
              }`}
            >
              {priorityLabels[issue.priority] ?? issue.priority}
            </span>
          )}
        </div>
      </div>
      </div>
    </div>
  );

  return cardNode;
}

// Loading skeleton for IssueCard
export function IssueCardSkeleton() {
  return (
    <div className="bg-warm-surface border border-warm-border rounded-lg p-3 shadow-sm animate-pulse">
      <div className="h-4 bg-warm-muted rounded w-3/4 mb-2" />
      <div className="h-3 bg-warm-muted/70 rounded w-full mb-3" />
      <div className="flex items-center justify-between">
        <div className="w-6 h-6 bg-warm-muted rounded-full" />
        <div className="h-4 bg-warm-muted/70 rounded w-12" />
      </div>
    </div>
  );
}

// Ghost placeholder shown during drag
export function IssueCardGhost() {
  const { t } = useTranslation('projects');
  return (
    <div className="border-2 border-dashed border-brand/50 rounded-lg p-3 bg-brand-soft min-h-[80px] flex items-center justify-center">
      <span className="text-xs text-brand">{t('kanban.card.dropHere')}</span>
    </div>
  );
}
