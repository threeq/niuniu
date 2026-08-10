import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api } from '@/lib/api';
import type { Repository } from '@/types/api';
import { toast } from 'sonner';
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog';
import { Input } from '@/components/ui/input';
import { Checkbox } from '@/components/ui/checkbox';

interface DeleteRepositoryDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  repository: Repository | null;
  onDeleted?: () => void;
}

export function DeleteRepositoryDialog({ open, onOpenChange, repository, onDeleted }: DeleteRepositoryDialogProps) {
  const { t } = useTranslation('repositories');
  const [confirmName, setConfirmName] = useState('');
  const [deleteDirectory, setDeleteDirectory] = useState(false);
  const [isDeleting, setIsDeleting] = useState(false);
  const queryClient = useQueryClient();

  const isConfirmValid = confirmName === repository?.name;

  const handleDelete = async () => {
    if (!repository || !isConfirmValid) return;

    setIsDeleting(true);
    try {
      const endpoint = deleteDirectory
        ? `/repositories/${repository.id}?delete_directory=true`
        : `/repositories/${repository.id}`;
      await api.delete(endpoint);
      queryClient.invalidateQueries({ queryKey: ['repositories'] });
      toast.success(t('dialogs.delete.successToast'));
      setConfirmName('');
      setDeleteDirectory(false);
      onOpenChange(false);
      onDeleted?.();
    } catch (error) {
      console.error('Failed to delete repository:', error);
      toast.error(t('dialogs.delete.errorToast'));
    } finally {
      setIsDeleting(false);
    }
  };

  const handleOpenChange = (open: boolean) => {
    if (!open) {
      setConfirmName('');
      setDeleteDirectory(false);
    }
    onOpenChange(open);
  };

  return (
    <AlertDialog open={open} onOpenChange={handleOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t('dialogs.delete.title')}</AlertDialogTitle>
        </AlertDialogHeader>
        <div className="py-4">
          <p className="text-sm text-muted-foreground mb-4">
            {t('dialogs.delete.confirmPrefix')}<strong>"{repository?.name}"</strong>{t('dialogs.delete.confirmSuffix')}
          </p>
          <p className="text-sm text-muted-foreground mb-2">
            {t('dialogs.delete.enterNamePromptPrefix')}<strong>"{repository?.name}"</strong>{t('dialogs.delete.enterNamePromptSuffix')}
          </p>
          <Input
            value={confirmName}
            onChange={(e) => setConfirmName(e.target.value)}
            placeholder={t('dialogs.delete.namePlaceholder')}
            disabled={isDeleting}
            className="mb-4"
          />

          <div className="flex items-start gap-2 p-3 bg-destructive/10 border border-destructive/30 rounded-lg">
            <Checkbox
              id="deleteDir"
              checked={deleteDirectory}
              onCheckedChange={(checked: boolean) => setDeleteDirectory(checked === true)}
              disabled={isDeleting}
              className="mt-0.5"
            />
            <label htmlFor="deleteDir" className="text-sm cursor-pointer">
              <span className="font-medium text-destructive">{t('dialogs.delete.deleteDirectory')}</span>
              <p className="text-destructive text-xs mt-0.5">
                {t('dialogs.delete.deleteDirectoryHint')}
              </p>
            </label>
          </div>
        </div>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={isDeleting}>{t('common:actions.cancel')}</AlertDialogCancel>
          <AlertDialogAction
            onClick={handleDelete}
            disabled={isDeleting || !isConfirmValid}
            className="bg-destructive hover:bg-destructive/90"
          >
            {isDeleting ? t('common:actions.deleting') : t('common:actions.delete')}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
