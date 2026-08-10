import { useEffect, useState } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { Plus, PanelLeftOpen } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { useWorkspaces } from '@/lib/hooks/use-workspaces';
import { useAppStore } from '@/stores/app';
import { useAuthStore } from '@/stores/auth-store';
import { useWorkspacePanelStore } from '@/stores/workspace-panel-store';
import { WorkspaceSidebar } from './workspace-sidebar';
import { NewWorkspaceDialog } from '@/components/dialogs/new-workspace-dialog';
import { OwnerFilter } from '@/components/shared/owner-filter';
import type { OwnerRef } from '@/types/org';

function SidebarExpandButton() {
  const { t } = useTranslation('workspaces');
  const toggleSidebar = useWorkspacePanelStore((s) => s.toggleSidebar);
  return (
    <button
      type="button"
      onClick={toggleSidebar}
      aria-label={t('sidebar.collapse.expand')}
      title={t('sidebar.collapse.expand')}
      className="self-start mt-2 ml-1 p-1 rounded text-muted-foreground hover:bg-accent hover:text-foreground transition-colors"
    >
      <PanelLeftOpen className="w-4 h-4" />
    </button>
  );
}

export function WorkspaceListPage() {
  const { t } = useTranslation('workspaces');
  const { workspaces, isLoading } = useWorkspaces();
  const { lastOpenedWorkspace } = useAppStore();
  const navigate = useNavigate();
  const [newWsOpen, setNewWsOpen] = useState(false);
  const currentUser = useAuthStore((s) => s.user);
  const userId = currentUser?.id ?? 0;
  const [ownerFilter, setOwnerFilter] = useState<OwnerRef[] | 'all'>('all');
  const isSidebarOpen = useWorkspacePanelStore((s) => s.isSidebarOpen);

  useEffect(() => {
    if (isLoading || !workspaces) return;

    // Try last opened
    if (lastOpenedWorkspace && workspaces.some((ws) => ws.id === lastOpenedWorkspace)) {
      navigate({ to: '/workspaces/$id', params: { id: lastOpenedWorkspace }, replace: true });
      return;
    }

    // Try first item
    if (workspaces.length > 0) {
      navigate({ to: '/workspaces/$id', params: { id: workspaces[0].id }, replace: true });
      return;
    }

    // Empty — stay on this page, show empty state (handled in render)
  }, [isLoading, workspaces, lastOpenedWorkspace, navigate]);

  // Show loading while data loads (prevents flash)
  if (isLoading) {
    return (
      <div className="flex h-full">
        {isSidebarOpen ? <WorkspaceSidebar /> : <SidebarExpandButton />}
        <div className="flex-1 flex items-center justify-center text-muted-foreground text-sm">
          {t('common:actions.loading')}
        </div>
      </div>
    );
  }

  // If workspaces exist, the useEffect will redirect — show sidebar while redirecting
  if (workspaces && workspaces.length > 0) {
    return (
      <div className="flex h-full">
        {isSidebarOpen ? <WorkspaceSidebar /> : <SidebarExpandButton />}
        <div className="flex-1" />
      </div>
    );
  }

  // Empty state
  return (
    <div className="flex h-full">
      {isSidebarOpen ? <WorkspaceSidebar /> : <SidebarExpandButton />}
      <div className="flex-1 flex flex-col items-center justify-center gap-3 text-muted-foreground">
        <OwnerFilter value={ownerFilter} onChange={setOwnerFilter} userId={userId} />
        <p className="text-sm">{t('list.empty')}</p>
        <button
          onClick={() => setNewWsOpen(true)}
          className="inline-flex items-center gap-1.5 px-3 py-1.5 text-sm bg-info text-white rounded-md hover:bg-info/90 transition-colors"
        >
          <Plus className="w-4 h-4" />
          {t('list.createWorkspace')}
        </button>
        <NewWorkspaceDialog open={newWsOpen} onOpenChange={setNewWsOpen} />
      </div>
    </div>
  );
}
