import { Link } from '@tanstack/react-router';
import { useTranslation } from 'react-i18next';
import { GitMerge, FileEdit, Loader2, GitBranch, Bell, User, Calendar, Clock, MessageSquare, Pin } from 'lucide-react';
import { cn } from '@/lib/utils';
import { getProjectColorStyles } from '@/lib/project-color';
import { Checkbox } from '@/components/ui/checkbox';
import { usePermissionPendingByWorkspace } from '@/stores/notification-ws-store';
import { useIsPinned, usePinnedWorkspacesStore } from '@/stores/pinned-workspaces-store';
import type { Workspace, Project } from '@/types/api';
import { WorkspaceBgTasksRow } from './workspace-bg-tasks-row';
import { WorkspaceIssueTypeMarker } from './workspace-sidebar-issue-type-marker';
import {
  getStatusDotClass,
  isStatusPulsing,
  formatTimeAgo,
  getStatusBadgeClass,
  formatShortDate,
  formatDuration,
  formatCompactCount,
} from './workspace-sidebar-helpers';

export interface WorkspaceCardProps {
  ws: Workspace;
  project: Project | undefined;
  isActive: boolean;
  // Batch-select mode: when on, the card stops navigating and toggles selection
  // instead. `selected` drives the checkbox + highlight; `onToggleSelect` fires
  // with this workspace's id (cascade to children is resolved by the caller).
  selectMode?: boolean;
  selected?: boolean;
  onToggleSelect?: (id: string) => void;
}

export function WorkspaceCard({
  ws,
  project,
  isActive,
  selectMode = false,
  selected = false,
  onToggleSelect,
}: WorkspaceCardProps) {
  const { t } = useTranslation('workspaces');
  const pcs = getProjectColorStyles(project?.color);
  const wsDotClass = getStatusDotClass(ws.status);
  const isDeleting = ws.status === 'deleting';
  const taskStats = ws.task_stats;
  const hasTask = taskStats && taskStats.total > 0;
  const currentTask = taskStats?.current_task;

  const hasGitInfo = ws.ahead_count > 0 || ws.changes_count > 0;
  const worktrees = ws.worktrees ?? [];
  const hasMultipleWorktrees = worktrees.length > 1;
  const pendingPermCount = usePermissionPendingByWorkspace(Number(ws.id));
  const creatorLabel = ws.creator_owner?.name ?? t('sidebar.card.creatorUnknown');
  const messageCount = ws.message_count ?? 0;
  const messageCountLabel = formatCompactCount(messageCount);
  const isPinned = useIsPinned(ws.id);
  const togglePin = usePinnedWorkspacesStore((s) => s.toggle);

  const content = (
    <>
      {/* Row 1: status dot + name + time */}
      <div className="flex items-center gap-1.5 min-w-0">
        <span
          className={cn(
            'w-1.5 h-1.5 rounded-full shrink-0',
            wsDotClass,
            isStatusPulsing(ws.status) && 'animate-pulse'
          )}
        />
        <span className="text-[10px] text-muted-foreground shrink-0 font-mono">!{ws.id}</span>
        <WorkspaceIssueTypeMarker issueType={ws.issue_type} parentIssueId={ws.parent_issue_id} />
        <span className="text-sm font-medium truncate flex-1">{ws.name}</span>
        <span
          className={cn(
            'inline-flex items-center gap-0.5 px-1 py-px rounded-sm text-[9px] font-semibold shrink-0',
            getStatusBadgeClass(ws.status)
          )}
        >
          {isDeleting && <Loader2 className="w-2 h-2 animate-spin" aria-hidden />}
          {t(`status.${ws.status}`, ws.status)}
        </span>
        {pendingPermCount > 0 && (
          <span
            className="flex items-center gap-0.5 rounded-full bg-amber-500/15 px-1.5 py-0.5 text-[10px] font-medium text-amber-600 shrink-0"
            title={t('permission.indicator.pendingCount', { count: pendingPermCount })}
            aria-label={t('permission.indicator.pendingCount', { count: pendingPermCount })}
          >
            <Bell className="w-2.5 h-2.5" />
            {pendingPermCount}
          </span>
        )}
        <span className="text-[10px] text-muted-foreground shrink-0">{formatTimeAgo(ws.updated_at)}</span>
        {/* Pin toggle — hidden until hover, but always shown while pinned so the
            pinned state is legible and one click unpins. Suppressed in select
            mode (the card is a non-navigating role=button there). */}
        {!selectMode && (
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
        )}
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

      {/* Row 2: per-worktree git status or aggregate */}
      {hasGitInfo && !hasMultipleWorktrees && (
        <div className="flex items-center gap-2 mt-0.5 text-[10px] leading-none pl-3">
          {ws.ahead_count > 0 && (
            <span className="flex items-center gap-0.5 text-info leading-none">
              <GitMerge className="w-2.5 h-2.5 shrink-0" />
              <span className="leading-none">{ws.ahead_count} ahead</span>
            </span>
          )}
          {ws.changes_count > 0 && (
            <span className="flex items-center gap-0.5 text-warning leading-none">
              <FileEdit className="w-2.5 h-2.5 shrink-0" />
              <span className="leading-none">{ws.changes_count} changes</span>
            </span>
          )}
        </div>
      )}
      {hasMultipleWorktrees && worktrees.some(wt => wt.ahead_count > 0 || wt.changes_count > 0) && (
        <div className="mt-0.5 pl-3 space-y-0.5">
          {worktrees.map((wt) => {
            if (wt.ahead_count === 0 && wt.changes_count === 0) return null;
            return (
              <div key={wt.name} className="flex items-center gap-1.5 text-[10px] leading-none">
                <GitBranch className="w-2.5 h-2.5 text-muted-foreground shrink-0" />
                <span className="text-muted-foreground truncate max-w-[60px] leading-none">{wt.repo_name}</span>
                {wt.ahead_count > 0 && (
                  <span className="flex items-center gap-0.5 text-info leading-none">
                    <GitMerge className="w-2.5 h-2.5 shrink-0" />
                    <span className="leading-none">{wt.ahead_count}</span>
                  </span>
                )}
                {wt.changes_count > 0 && (
                  <span className="flex items-center gap-0.5 text-warning leading-none">
                    <FileEdit className="w-2.5 h-2.5 shrink-0" />
                    <span className="leading-none">{wt.changes_count}</span>
                  </span>
                )}
              </div>
            );
          })}
        </div>
      )}

      {/* Row 3: project · task progress */}
      {(ws.project_name || hasTask) && (
        <div className="flex items-center gap-1 mt-0.5 min-w-0 text-[10px] leading-none text-muted-foreground pl-3">
          {ws.project_name && (
            <>
              <span className="truncate max-w-[60px] leading-none">{ws.project_name}</span>
              {hasTask && <span className="leading-none">·</span>}
            </>
          )}
          {hasTask && (
            <>
              {currentTask && <Loader2 className="w-2.5 h-2.5 animate-spin shrink-0 text-info" />}
              <span className={cn(
                'leading-none',
                currentTask ? 'text-info' : 'text-muted-foreground'
              )}>
                {taskStats.completed}/{taskStats.total}
              </span>
              {currentTask && (
                <span className="truncate max-w-[60px] text-info/70 leading-none">{currentTask}</span>
              )}
            </>
          )}
        </div>
      )}
      <WorkspaceBgTasksRow data={ws.bg_tasks} />
    </>
  );

  const baseClass = cn(
    'group block px-3 py-1.5 rounded-md mx-1 border-l-2 transition-colors text-foreground',
    isDeleting && 'opacity-60'
  );

  // Batch-select mode: a non-navigating card that toggles selection. A checkbox
  // (a <button>) can't live inside the <Link> (invalid nested interactive),
  // so select mode renders a <div role="button"> instead.
  if (selectMode) {
    return (
      <div
        role="button"
        tabIndex={0}
        aria-pressed={selected}
        onClick={() => onToggleSelect?.(ws.id)}
        onKeyDown={(e) => {
          if (e.key === ' ' || e.key === 'Enter') {
            e.preventDefault();
            onToggleSelect?.(ws.id);
          }
        }}
        className={cn(
          baseClass,
          'cursor-pointer',
          selected
            ? cn('font-medium', pcs.bgActive, pcs.borderActive)
            : cn('hover:bg-accent border-transparent', pcs.bgInactive)
        )}
      >
        <div className="flex items-start gap-1.5">
          <Checkbox
            checked={selected}
            tabIndex={-1}
            aria-hidden
            className="mt-0.5 shrink-0 pointer-events-none"
          />
          <div className="flex-1 min-w-0">{content}</div>
        </div>
      </div>
    );
  }

  return (
    <Link
      to="/workspaces/$id"
      params={{ id: ws.id }}
      className={cn(
        baseClass,
        isActive
          ? cn('font-medium', pcs.bgActive, pcs.borderActive)
          : cn('hover:bg-accent border-transparent', pcs.bgInactive)
      )}
    >
      {content}
    </Link>
  );
}
