import { useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api } from '@/lib/api';
import type { Issue } from '@/types/api';
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

interface DeleteIssueDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  issue: Issue | null;
  onDeleted?: () => void;
  onSuccess?: () => void;
  projectId?: string;
}

export function DeleteIssueDialog({ open, onOpenChange, issue, onDeleted, onSuccess }: DeleteIssueDialogProps) {
  const { t } = useTranslation('projects');
  const [isDeleting, setIsDeleting] = useState(false);
  const queryClient = useQueryClient();

  const deleteMutation = useMutation({
    mutationFn: (id: number) => api.delete<void>(`/issues/${id}`),
    onSuccess: () => {
      if (issue?.column_id) {
        queryClient.invalidateQueries({ queryKey: ['issues', issue.column_id] });
      }
      queryClient.invalidateQueries({ queryKey: ['issues'] });
      // Invalidate all kanban queries so the board refreshes
      queryClient.invalidateQueries({ queryKey: ['all-issues'] });
      onOpenChange(false);
      onDeleted?.();
      onSuccess?.();
    },
  });

  const handleDelete = async () => {
    if (!issue) return;

    setIsDeleting(true);
    try {
      await deleteMutation.mutateAsync(issue.id);
    } catch (error) {
      console.error('Failed to delete issue:', error);
    } finally {
      setIsDeleting(false);
    }
  };

  if (!issue) return null;

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t('deleteIssue.title')}</AlertDialogTitle>
          <AlertDialogDescription>
            {t('deleteIssue.description', { title: issue.title })}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={isDeleting}>{t('deleteIssue.cancel')}</AlertDialogCancel>
          <AlertDialogAction
            onClick={handleDelete}
            disabled={isDeleting}
            className="bg-destructive hover:bg-destructive/90"
          >
            {isDeleting ? t('deleteIssue.deleting') : t('deleteIssue.delete')}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
