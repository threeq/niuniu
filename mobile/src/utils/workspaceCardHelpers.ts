import type { ThemeColors } from '../theme/tokens';
import type { BgTaskAggregateDTO, Workspace } from '../api/types';

// Union of server WorkspaceStatus + agent-process fallbacks (failed / idle).
// Mirrors mobile/src/components/StatusBadge.tsx BadgeStatus.
export type BadgeStatus =
  | 'running'
  | 'completed'
  | 'failed'
  | 'idle'
  | 'needs_review'
  | 'attention'
  | 'created';

// Workflow status (workspaces.status) takes priority — those are the
// human-facing workflow states (needs_review on agent done, attention on
// agent error or review timeout). Falls back to agent_status only when
// status is the base value, to cover transition windows where the server
// has not yet shifted the workspace.
export function mapWorkspaceStatus(ws: Workspace): BadgeStatus {
  if (ws.status === 'attention') return 'attention';
  if (ws.status === 'needs_review') return 'needs_review';
  if (ws.status === 'running') return 'running';
  if (ws.status === 'completed') return 'completed';
  if (ws.status === 'created') return 'created';
  if (ws.agent_status === 'running') return 'running';
  if (ws.agent_status === 'error') return 'failed';
  if (ws.agent_status === 'completed') return 'completed';
  return 'idle';
}

export interface IdBadgeColors {
  bg: string;
  text: string;
}

// getIdBadgeColors maps workspace status to ID-pill background + text.
// Accepts the BadgeStatus union produced by mapWorkspaceStatus on mobile
// (workspaces.status with agent_status fallback), so it must handle
// `completed` and `failed` in addition to the workspaces.status values.
export function getIdBadgeColors(
  status: string | undefined,
  colors: ThemeColors,
): IdBadgeColors {
  switch (status) {
    case 'running':
    case 'completed':
      return { bg: colors.status.success, text: '#FFFFFF' };
    case 'attention':
    case 'failed':
      return { bg: colors.status.error, text: '#FFFFFF' };
    case 'needs_review':
    case 'created':
      return { bg: colors.status.warning, text: '#FFFFFF' };
    default:
      return { bg: colors.bg.muted, text: colors.text.secondary };
  }
}

// hasAnyBgTask returns true when the bg-tasks row should render.
// Mirrors workspace-bg-tasks-row.tsx's hasAny check.
export function hasAnyBgTask(data: BgTaskAggregateDTO | undefined): boolean {
  if (!data) return false;
  return (
    data.agent_busy ||
    data.bash_count > 0 ||
    data.wakeup_count > 0 ||
    data.subagent_count > 0 ||
    data.cron_count > 0
  );
}
