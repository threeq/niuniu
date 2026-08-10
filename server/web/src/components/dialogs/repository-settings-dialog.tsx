import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { Repository } from '@/types/api';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import { DeleteRepositoryDialog } from './delete-repository-dialog';

interface RepositorySettingsDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  repository: Repository | null;
  onDeleted?: () => void;
}

export function RepositorySettingsDialog({ open, onOpenChange, repository, onDeleted }: RepositorySettingsDialogProps) {
  const { t } = useTranslation('repositories');
  const [showDeleteDialog, setShowDeleteDialog] = useState(false);

  const handleDeleteSuccess = () => {
    setShowDeleteDialog(false);
    onOpenChange(false);
    onDeleted?.();
  };

  const handleClose = () => {
    onOpenChange(false);
  };

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent className="sm:max-w-[425px]">
          <DialogHeader>
            <DialogTitle>{t('dialogs.settings.title')}</DialogTitle>
            <DialogDescription>
              {t('dialogs.settings.description', { name: repository?.name ?? '' })}
            </DialogDescription>
          </DialogHeader>
          <div className="py-4">
            <Button
              variant="destructive"
              onClick={() => setShowDeleteDialog(true)}
              className="w-full"
            >
              {t('dialogs.settings.deleteRepository')}
            </Button>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={handleClose}>
              {t('common:actions.close')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <DeleteRepositoryDialog
        open={showDeleteDialog}
        onOpenChange={setShowDeleteDialog}
        repository={repository}
        onDeleted={handleDeleteSuccess}
      />
    </>
  );
}
