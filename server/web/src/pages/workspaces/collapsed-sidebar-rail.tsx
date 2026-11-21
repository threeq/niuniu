import { useState } from 'react';
import { PanelLeftOpen, Plus } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { useWorkspacePanelStore } from '@/stores/workspace-panel-store';
import { NewWorkspaceDialog } from '@/components/dialogs/new-workspace-dialog';

/** Narrow rail shown when the session sidebar is collapsed. Keeps expand and
 *  create reachable without forcing the user to reopen the full sidebar. */
export function CollapsedSidebarRail() {
  const { t } = useTranslation('workspaces');
  const toggleSidebar = useWorkspacePanelStore((s) => s.toggleSidebar);
  const [newOpen, setNewOpen] = useState(false);

  return (
    <div className="flex w-9 shrink-0 flex-col items-center gap-1 border-r border-warm-border bg-warm-muted py-2">
      <button
        type="button"
        onClick={toggleSidebar}
        aria-label={t('sidebar.collapse.expand')}
        title={t('sidebar.collapse.expand')}
        className="grid h-7 w-7 place-items-center rounded text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
      >
        <PanelLeftOpen className="w-4 h-4" />
      </button>
      <button
        type="button"
        onClick={() => setNewOpen(true)}
        aria-label={t('sidebar.newWorkspace')}
        title={t('sidebar.newWorkspace')}
        className="grid h-7 w-7 place-items-center rounded bg-primary text-primary-foreground transition-colors hover:bg-primary/90"
      >
        <Plus className="w-4 h-4" />
      </button>
      <NewWorkspaceDialog open={newOpen} onOpenChange={setNewOpen} />
    </div>
  );
}
