import { useState } from 'react';
import { Layers } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog';
import { WorkspaceScenesPanel } from './workspace-scenes-panel';

interface WorkspaceScenesDialogProps {
  workspaceId: number;
}

// Chat-input-footer companion of WorkspaceScenesPanel. The dialog button sits
// alongside the workspace / Claude / permission settings in the input bar so
// scene management is reachable from where a user composes their next turn,
// matching how 工作空间 / Claude / 权限白名单 dialogs are positioned.
export function WorkspaceScenesDialog({ workspaceId }: WorkspaceScenesDialogProps) {
  const { t } = useTranslation('workspaces');
  const [open, setOpen] = useState(false);

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <button
          className="flex items-center gap-1 rounded px-2 py-1 text-xs text-muted-foreground hover:bg-accent hover:text-foreground transition-colors"
          title={t('panels.workspaceScenes.triggerTitle')}
        >
          <Layers className="h-3.5 w-3.5" />
          <span>{t('panels.workspaceScenes.trigger')}</span>
        </button>
      </DialogTrigger>
      <DialogContent className="max-w-3xl max-h-[85vh]">
        <DialogHeader>
          <DialogTitle>{t('panels.workspaceScenes.title')}</DialogTitle>
          <DialogDescription>{t('panels.workspaceScenes.description')}</DialogDescription>
        </DialogHeader>
        <WorkspaceScenesPanel workspaceId={workspaceId} />
      </DialogContent>
    </Dialog>
  );
}
