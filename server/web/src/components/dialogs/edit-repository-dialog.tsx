import { useState, useEffect } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api } from '@/lib/api';
import type { Repository } from '@/types/api';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Button } from '@/components/ui/button';

interface EditRepositoryDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  repository: Repository | null;
}

export function EditRepositoryDialog({ open, onOpenChange, repository }: EditRepositoryDialogProps) {
  const { t } = useTranslation('repositories');
  const [name, setName] = useState('');
  const [path, setPath] = useState('');
  const [defaultBranch, setDefaultBranch] = useState('');
  const [isSubmitting, setIsSubmitting] = useState(false);
  const queryClient = useQueryClient();

  useEffect(() => {
    if (repository) {
      setName(repository.name);
      setPath(repository.path);
      setDefaultBranch(repository.default_branch || 'main');
    }
  }, [repository]);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim() || !path.trim() || !repository) return;

    setIsSubmitting(true);
    try {
      await api.put(`/repositories/${repository.id}`, {
        name,
        path,
        default_branch: defaultBranch,
      });
      queryClient.invalidateQueries({ queryKey: ['repositories'] });
      onOpenChange(false);
    } catch (error) {
      console.error('Failed to update repository:', error);
    } finally {
      setIsSubmitting(false);
    }
  };

  const handleOpenChange = (newOpen: boolean) => {
    if (!newOpen) {
      setName('');
      setPath('');
      setDefaultBranch('main');
    }
    onOpenChange(newOpen);
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="sm:max-w-[425px]">
        <DialogHeader>
          <DialogTitle>{t('dialogs.edit.title')}</DialogTitle>
          <DialogDescription>
            {t('dialogs.edit.description')}
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={handleSubmit}>
          <div className="grid gap-4 py-4">
            <div className="grid gap-2">
              <label htmlFor="edit-repository-name" className="text-sm font-medium">
                {t('dialogs.edit.nameLabel')} <span className="text-destructive">*</span>
              </label>
              <Input
                id="edit-repository-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t('dialogs.edit.namePlaceholder')}
                disabled={isSubmitting}
              />
            </div>
            <div className="grid gap-2">
              <label htmlFor="edit-repository-path" className="text-sm font-medium">
                {t('dialogs.edit.pathLabel')} <span className="text-destructive">*</span>
              </label>
              <Input
                id="edit-repository-path"
                value={path}
                onChange={(e) => setPath(e.target.value)}
                placeholder={t('dialogs.edit.pathPlaceholder')}
                disabled={isSubmitting}
              />
            </div>
            <div className="grid gap-2">
              <label htmlFor="edit-repository-branch" className="text-sm font-medium">
                {t('dialogs.edit.branchLabel')}
              </label>
              <Input
                id="edit-repository-branch"
                value={defaultBranch}
                onChange={(e) => setDefaultBranch(e.target.value)}
                placeholder={t('dialogs.edit.branchPlaceholder')}
                disabled={isSubmitting}
              />
            </div>
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => handleOpenChange(false)}
              disabled={isSubmitting}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" disabled={isSubmitting || !name.trim() || !path.trim()}>
              {isSubmitting ? t('common:actions.saving') : t('common:actions.save')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
