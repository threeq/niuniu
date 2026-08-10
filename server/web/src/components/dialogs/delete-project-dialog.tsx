import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api } from '@/lib/api';
import { useNavigate } from '@tanstack/react-router';
import type { Project } from '@/types/api';
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog';

interface DeleteProjectDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  project: Project | null;
}

export function DeleteProjectDialog({ open, onOpenChange, project }: DeleteProjectDialogProps) {
  const { t } = useTranslation('projects');
  const [isDeleting, setIsDeleting] = useState(false);
  const queryClient = useQueryClient();
  const navigate = useNavigate();

  const handleDelete = async () => {
    if (!project) return;

    setIsDeleting(true);
    try {
      await api.delete(`/projects/${project.id}`);
      queryClient.invalidateQueries({ queryKey: ['projects'] });
      onOpenChange(false);
      navigate({ to: '/projects', replace: true });
    } catch (error) {
      console.error('Failed to delete project:', error);
    } finally {
      setIsDeleting(false);
    }
  };

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle className="text-destructive">{t('deleteProject.title')}</AlertDialogTitle>
          <AlertDialogDescription className="space-y-2">
            <p>
              {t('deleteProject.confirm')}<strong>{t('deleteProject.openQuote')}{project?.name}{t('deleteProject.closeQuote')}</strong>{t('deleteProject.confirmSuffix')}
            </p>
            <p className="text-destructive font-medium">
              {t('deleteProject.warning')}
            </p>
            <p className="text-xs text-muted-foreground">
              {t('deleteProject.hint')}
            </p>
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={isDeleting}>{t('deleteProject.cancel')}</AlertDialogCancel>
          <AlertDialogAction
            onClick={(e) => { e.preventDefault(); handleDelete(); }}
            disabled={isDeleting}
            className="bg-destructive hover:bg-destructive/90"
          >
            {isDeleting ? t('deleteProject.deleting') : t('deleteProject.deleteForever')}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
