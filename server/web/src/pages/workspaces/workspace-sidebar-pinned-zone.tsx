import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Pin, ChevronDown, ChevronRight } from 'lucide-react';
import { cn } from '@/lib/utils';
import { useAuthStore } from '@/stores/auth-store';
import { usePinnedWorkspacesStore } from '@/stores/pinned-workspaces-store';
import { WorkspaceCard } from './workspace-sidebar-card';
import type { Workspace, Project } from '@/types/api';

// Whole-zone collapse, persisted per user (mirrors the priority zone's own
// per-user localStorage prefs). One boolean key — keep the surface small.
const COLLAPSE_KEY = 'sidebarPinnedCollapsed';
const collapseKeyFor = (uid: number) => `${COLLAPSE_KEY}:u${uid}`;

export interface PinnedZoneProps {
  workspaces: Workspace[]; // 父组件已按 scope + search 过滤
  projectsByKey: Map<string, Project>;
  projectKeyForWs: (ws: Workspace) => string;
  activeWorkspaceId?: string;
}

export function PinnedZone({
  workspaces,
  projectsByKey,
  projectKeyForWs,
  activeWorkspaceId,
}: PinnedZoneProps) {
  const { t } = useTranslation('workspaces');
  const currentUserId = useAuthStore((s) => s.user?.id ?? 0);
  const pinnedIds = usePinnedWorkspacesStore((s) => s.pinnedIds);

  const [collapsed, setCollapsed] = useState<boolean>(() => {
    try {
      return localStorage.getItem(collapseKeyFor(currentUserId)) === '1';
    } catch {
      return false;
    }
  });
  const toggleCollapsed = () => {
    setCollapsed((prev) => {
      const next = !prev;
      try {
        localStorage.setItem(collapseKeyFor(currentUserId), next ? '1' : '0');
      } catch {
        /* ignore */
      }
      return next;
    });
  };

  // Resolve pinned ids to the workspaces present in the current (filtered) list,
  // preserving pin order (most-recently-pinned first). A pinned workspace not in
  // the current scope/search view simply doesn't show until it's back in view.
  const pinned = useMemo(() => {
    const byId = new Map(workspaces.map((ws) => [String(ws.id), ws]));
    return pinnedIds
      .map((id) => byId.get(id))
      .filter((ws): ws is Workspace => ws !== undefined);
  }, [workspaces, pinnedIds]);

  if (pinned.length === 0) return null;

  return (
    <div className="px-3.5 py-2.5 border-b border-warm-border bg-gradient-to-b from-brand/[0.04] to-transparent">
      <div className={cn('flex items-center justify-between', !collapsed && 'mb-2')}>
        <button
          type="button"
          onClick={toggleCollapsed}
          className="inline-flex items-center gap-1.5 text-[11px] font-semibold
                     uppercase tracking-wider text-foreground hover:text-foreground/80 transition-colors"
          aria-expanded={!collapsed}
        >
          {collapsed ? (
            <ChevronRight className="w-3 h-3 text-muted-foreground" />
          ) : (
            <ChevronDown className="w-3 h-3 text-muted-foreground" />
          )}
          <Pin className="w-3 h-3 text-brand" />
          {t('sidebar.pinned.zoneTitle')}
          <span className="px-1.5 py-0.5 rounded-full bg-brand/15 text-brand text-[10px] font-semibold">
            {pinned.length}
          </span>
        </button>
      </div>
      {!collapsed && (
        <div className="space-y-0.5">
          {pinned.map((ws) => (
            <WorkspaceCard
              key={ws.id}
              ws={ws}
              project={projectsByKey.get(projectKeyForWs(ws))}
              isActive={String(activeWorkspaceId) === String(ws.id)}
            />
          ))}
        </div>
      )}
    </div>
  );
}
