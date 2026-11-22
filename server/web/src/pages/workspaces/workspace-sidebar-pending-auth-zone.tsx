import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { ShieldAlert } from 'lucide-react';
import { useNotificationWSStore } from '@/stores/notification-ws-store';
import { WorkspaceCard } from './workspace-sidebar-card';
import type { Workspace, Project } from '@/types/api';

export interface PendingAuthZoneProps {
  workspaces: Workspace[]; // 父组件已按 scope + search 过滤
  projectsByKey: Map<string, Project>;
  projectKeyForWs: (ws: Workspace) => string;
  activeWorkspaceId?: string;
}

/**
 * Sessions whose agent is blocked on a permission request, surfaced as their
 * own group above the pinned zone so the user can jump straight to approving.
 * Fed by the cross-page notify channel (`permission_pending` notify events),
 * so the group is correct from any page, not just the active workspace.
 * Hidden entirely while nothing needs authorization.
 */
export function PendingAuthZone({
  workspaces,
  projectsByKey,
  projectKeyForWs,
  activeWorkspaceId,
}: PendingAuthZoneProps) {
  const { t } = useTranslation('workspaces');
  const pendingByWorkspace = useNotificationWSStore((s) => s.permissionPendingByWorkspace);

  const pending = useMemo(
    () =>
      workspaces
        .filter((ws) => (pendingByWorkspace[Number(ws.id)]?.size ?? 0) > 0)
        .sort((a, b) => Number(b.id) - Number(a.id)),
    [workspaces, pendingByWorkspace],
  );

  if (pending.length === 0) return null;

  return (
    <div
      data-sidebar-zone="pending-auth"
      className="px-3.5 py-2.5 border-b border-warm-border bg-gradient-to-b from-warning/[0.06] to-transparent"
    >
      <div className="flex items-center justify-between mb-2">
        <div className="inline-flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-foreground">
          <ShieldAlert className="w-3 h-3 text-warning" />
          {t('sidebar.pendingAuth.zoneTitle')}
          <span className="px-1.5 py-0.5 rounded-full bg-warning/15 text-warning text-[10px] font-semibold">
            {pending.length}
          </span>
        </div>
      </div>
      <div className="space-y-0.5">
        {pending.map((ws) => (
          <WorkspaceCard
            key={ws.id}
            ws={ws}
            project={projectsByKey.get(projectKeyForWs(ws))}
            isActive={String(activeWorkspaceId) === String(ws.id)}
          />
        ))}
      </div>
    </div>
  );
}
