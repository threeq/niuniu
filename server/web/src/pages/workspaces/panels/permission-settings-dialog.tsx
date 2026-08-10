import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ShieldCheck } from 'lucide-react';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog';
import { PermissionAllowlistTab } from './permission-allowlist-tab';

interface PermissionSettingsDialogProps {
  workspaceId: string;
}

export function PermissionSettingsDialog({ workspaceId }: PermissionSettingsDialogProps) {
  const { t } = useTranslation('workspaces');
  const [open, setOpen] = useState(false);

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <button
          className="flex items-center gap-1 rounded px-2 py-1 text-xs text-muted-foreground hover:bg-accent hover:text-foreground transition-colors"
          title={t('permission.allowlist.title')}
        >
          <ShieldCheck className="h-3.5 w-3.5" />
          <span>{t('permission.allowlist.title')}</span>
        </button>
      </DialogTrigger>
      <DialogContent className="max-w-lg max-h-[85vh]">
        <DialogHeader>
          <DialogTitle>{t('permission.allowlist.title')}</DialogTitle>
        </DialogHeader>
        <PermissionAllowlistTab workspaceId={Number(workspaceId)} />
      </DialogContent>
    </Dialog>
  );
}
