import { Link } from '@tanstack/react-router';
import { useTranslation } from 'react-i18next';
import { Check, GitMerge, FileEdit, User, Calendar, Clock, MessageSquare, Pin } from 'lucide-react';
import { cn } from '@/lib/utils';
import { useIsPinned, usePinnedWorkspacesStore } from '@/stores/pinned-workspaces-store';
import {
  getStatusDotClass,
  isStatusPulsing,
  formatTimeAgo,
  formatShortDate,
  formatDuration,
  formatCompactCount,
  getPriorityKind,
  type PriorityKind,
} from './workspace-sidebar-helpers';
import { WorkspaceBgTasksRow } from './workspace-bg-tasks-row';
import { WorkspaceIssueTypeMarker } from './workspace-sidebar-issue-type-marker';
import { getProjectColorStyles } from '@/lib/project-color';
import type { Workspace, Project } from '@/types/api';

const tagClass: Record<PriorityKind, string> = {
  attention: 'bg-destructive/15 text-destructive',
  needs_review: 'bg-warning/15 text-warning',
  running: 'bg-success/15 text-success',
  not_started: 'bg-info/15 text-info',
};

const labelKey: Record<PriorityKind, string> = {
  attention: 'sidebar.priority.attention',
  needs_review: 'sidebar.priority.needsReview',
  running: 'sidebar.priority.running',
  not_started: 'sidebar.priority.notStarted',
};

export interface PriorityCardProps {
  ws: Workspace;
  project: Project | undefined;
  isActive: boolean;
  onMarkDone: (id: string | number) => void;
  /** When the parent's useMarkWorkspaceDone reports this workspace's id in
   * its pendingIds set, disable the button + dim it so users see the
   * in-flight mark-done request (and so the hook's internal dedupe guard
   * isn't the only signal). */
  isPending?: boolean;
}

export function PriorityCard({ ws, project, isActive, onMarkDone, isPending = false }: PriorityCardProps) {
  const { t } = useTranslation('workspaces');
  const kind = getPriorityKind(ws);
  const isPinned = useIsPinned(ws.id);
  const togglePin = usePinnedWorkspacesStore((s) => s.toggle);

  if (!kind) return null;

  const canComplete = kind !== 'running';
  const pcs = getProjectColorStyles(project?.color);
  const creatorLabel = ws.creator_owner?.name ?? t('sidebar.card.creatorUnknown');
  const messageCount = ws.message_count ?? 0;
  const messageCountLabel = formatCompactCount(messageCount);

  return (
    <Link
      to="/workspaces/$id"
      params={{ id: ws.id }}
      className={cn(
        'group block px-2.5 py-2 rounded-md border-l-2 transition-colors',
        isActive
          ? cn(pcs.borderActive, pcs.bgActive)
          : cn('border-transparent', pcs.bgInactive, 'hover:bg-accent')
      )}
    >
      {/* Row 1 */}
      <div className="flex items-center gap-1.5 min-w-0">
        <span
          className={cn(
            'w-1.5 h-1.5 rounded-full shrink-0',
            getStatusDotClass(ws.status),
            isStatusPulsing(ws.status) && 'animate-pulse'
          )}
        />
        <span className="text-[10px] text-muted-foreground shrink-0 font-mono">!{ws.id}</span>
        <WorkspaceIssueTypeMarker issueType={ws.issue_type} parentIssueId={ws.parent_issue_id} />
        <span className="text-xs font-medium truncate flex-1">{ws.name}</span>
        <span className="text-[10px] text-muted-foreground shrink-0">{formatTimeAgo(ws.updated_at)}</span>
        {/* Pin toggle — hidden until hover, always shown while pinned. Mirrors
            the regular sidebar card so priority cards can be pinned too. */}
        <button
          type="button"
          onClick={(e) => {
            e.preventDefault();
            e.stopPropagation();
            togglePin(ws.id);
          }}
          aria-pressed={isPinned}
          aria-label={t(isPinned ? 'sidebar.pinned.unpin' : 'sidebar.pinned.pin')}
          title={t(isPinned ? 'sidebar.pinned.unpin' : 'sidebar.pinned.pin')}
          className={cn(
            'shrink-0 -my-0.5 p-0.5 rounded transition-colors',
            'hover:bg-warm-border focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40',
            isPinned
              ? 'text-brand opacity-100'
              : 'text-muted-foreground opacity-0 group-hover:opacity-100 focus-visible:opacity-100'
          )}
        >
          <Pin className={cn('w-3 h-3', isPinned && 'fill-brand')} />
        </button>
      </div>
      {/* Creator row — creator · created date · run duration */}
      <div className="flex items-center gap-1 mt-0.5 text-[10px] leading-none text-muted-foreground pl-3 min-w-0">
        <User className="w-2.5 h-2.5 shrink-0" />
        <span className="truncate min-w-0 leading-none">{creatorLabel}</span>
        {messageCount > 0 && (
          <span
            className="flex items-center gap-0.5 shrink-0 tabular-nums leading-none"
            title={t('sidebar.card.lastMessageLabel')}
          >
            <MessageSquare className="w-2.5 h-2.5 shrink-0" />
            <span className="leading-none">{messageCountLabel}</span>
          </span>
        )}
        <span
          className="flex items-center gap-0.5 shrink-0 tabular-nums leading-none"
          title={t('sidebar.card.createdAtLabel')}
        >
          <Calendar className="w-2.5 h-2.5 shrink-0" />
          <span className="leading-none">{formatShortDate(ws.created_at)}</span>
        </span>
        <span
          className="flex items-center gap-0.5 shrink-0 tabular-nums leading-none"
          title={t('sidebar.card.durationLabel')}
        >
          <Clock className="w-2.5 h-2.5 shrink-0" />
          <span className="leading-none">{formatDuration(ws.created_at, ws.updated_at)}</span>
        </span>
      </div>
      {/* Row 2 */}
      <div className="flex items-center gap-1 mt-1 text-[10px] leading-none text-muted-foreground">
        {ws.project_name && (
          <span className={cn('px-1 py-px rounded-sm text-[9px] font-medium leading-none', pcs.bgInactive, pcs.text)}>
            {ws.project_name}
          </span>
        )}
        <span className={cn('px-1 py-px rounded-sm text-[9px] font-semibold leading-none', tagClass[kind])}>
          {t(labelKey[kind])}
        </span>
        {ws.ahead_count > 0 && (
          <span className="inline-flex items-center gap-0.5 text-info text-[9px] tabular-nums leading-none">
            <GitMerge className="w-2.5 h-2.5 shrink-0" />
            <span className="leading-none">{ws.ahead_count}</span>
          </span>
        )}
        {ws.changes_count > 0 && (
          <span className="inline-flex items-center gap-0.5 text-warning text-[9px] tabular-nums leading-none">
            <FileEdit className="w-2.5 h-2.5 shrink-0" />
            <span className="leading-none">{ws.changes_count}</span>
          </span>
        )}
        {/* Right cluster on the status row: bg-tasks activity indicators +
            (optional) 完成 button. One ml-auto on the wrapper pins both to
            the right edge regardless of which subset is present. */}
        <div className="ml-auto flex items-center gap-1.5 shrink-0">
          <WorkspaceBgTasksRow data={ws.bg_tasks} inline />
          {canComplete && (
            <button
              type="button"
              disabled={isPending}
              onClick={(e) => {
                e.preventDefault();
                e.stopPropagation();
                if (isPending) return;
                onMarkDone(ws.id);
              }}
              className={cn(
                'inline-flex items-center gap-0.5 px-2 py-0.5 rounded-full',
                'text-[10px] font-semibold text-success',
                'bg-success/15 border border-success/30',
                'hover:bg-success/20 transition-colors',
                isPending && 'opacity-50 cursor-not-allowed'
              )}
            >
              <Check className="w-3 h-3" />
              {t('sidebar.markDone.button')}
            </button>
          )}
        </div>
      </div>
    </Link>
  );
}
