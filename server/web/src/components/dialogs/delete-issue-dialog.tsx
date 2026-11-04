import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { apiFetch, ApiError } from '@/lib/api';
import type { Issue, WorktreeChangeStatus } from '@/types/api';
import { DeleteWorkspaceDialog } from './delete-workspace-dialog';
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
  // Deleting an issue also deletes its workspace. When that workspace has
  // uncommitted/unmerged changes the server answers 409 with the change list —
  // the SAME contract as workspace deletion — and we surface it in the shared
  // DeleteWorkspaceDialog before re-sending with force.
  const [pendingChanges, setPendingChanges] = useState<WorktreeChangeStatus[] | null>(null);
  const queryClient = useQueryClient();

  const afterDeleted = () => {
    if (issue?.column_id) {
      queryClient.invalidateQueries({ queryKey: ['issues', issue.column_id] });
    }
    queryClient.invalidateQueries({ queryKey: ['issues'] });
    // Invalidate all kanban queries so the board refreshes
    queryClient.invalidateQueries({ queryKey: ['all-issues'] });
    queryClient.invalidateQueries({ queryKey: ['workspaces'] });
    queryClient.invalidateQueries({ queryKey: ['workspace-available-issues'] });
    setPendingChanges(null);
    onOpenChange(false);
    onDeleted?.();
    onSuccess?.();
  };

  const runDelete = async (force: boolean) => {
    if (!issue) return;
    setIsDeleting(true);
    try {
      await apiFetch<void>(`/issues/${issue.id}${force ? '?force=true' : ''}`, {
        method: 'DELETE',
      });
      afterDeleted();
    } catch (error) {
      if (!force && error instanceof ApiError && error.status === 409) {
        const details = (error.body as { error?: { details?: unknown } })?.error?.details;
        setPendingChanges((details ?? []) as WorktreeChangeStatus[]);
        return;
      }
      console.error('Failed to delete issue:', error);
    } finally {
      setIsDeleting(false);
    }
  };

  const handleDelete = () => void runDelete(false);

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

      {/* Workspace changes that issue deletion would destroy: same dialog the
          workspace delete flow uses. Confirming here re-sends with force. */}
      <DeleteWorkspaceDialog
        open={pendingChanges !== null}
        onOpenChange={(o) => {
          if (!o) setPendingChanges(null);
        }}
        changes={pendingChanges ?? []}
        onForceDelete={() => void runDelete(true)}
        isDeleting={isDeleting}
      />
    </AlertDialog>
  );
}
