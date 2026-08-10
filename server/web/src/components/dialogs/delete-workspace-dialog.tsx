import { useTranslation } from 'react-i18next';
import type { WorktreeChangeStatus } from '@/types/api';
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog';

interface DeleteWorkspaceDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  changes: WorktreeChangeStatus[];
  onForceDelete: () => void;
  isDeleting: boolean;
}

export function DeleteWorkspaceDialog({ open, onOpenChange, changes, onForceDelete, isDeleting }: DeleteWorkspaceDialogProps) {
  const { t } = useTranslation('workspaces');
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent className="max-w-lg max-h-[80vh] overflow-hidden flex flex-col">
        <AlertDialogHeader>
          <AlertDialogTitle>{t('dialogs.deleteWorkspace.title')}</AlertDialogTitle>
        </AlertDialogHeader>
        <div className="flex-1 overflow-y-auto py-2 space-y-3">
          {changes.map((change, idx) => (
            <div key={idx} className="rounded-md border border-border p-3 space-y-2">
              <div className="text-sm font-medium text-foreground">
                {change.repo_name}
                <span className="font-mono text-xs text-muted-foreground ml-1">
                  ({change.branch}{change.base_branch ? ` ← ${change.base_branch}` : ''})
                </span>
              </div>

              {/* Unstaged files */}
              {change.unstaged_files.length > 0 && (
                <div>
                  <p className="text-xs font-medium text-muted-foreground mb-1">
                    {t('dialogs.deleteWorkspace.unstaged', { count: change.unstaged_files.length })}
                  </p>
                  <div className="space-y-0.5">
                    {change.unstaged_files.map((f, i) => (
                      <p key={i} className="text-xs font-mono text-muted-foreground pl-2 truncate">
                        <span className="text-muted-foreground/70 mr-1.5">{f.status}</span>{f.path}
                      </p>
                    ))}
                  </div>
                </div>
              )}

              {/* Staged files */}
              {change.staged_files.length > 0 && (
                <div>
                  <p className="text-xs font-medium text-muted-foreground mb-1">
                    {t('dialogs.deleteWorkspace.staged', { count: change.staged_files.length })}
                  </p>
                  <div className="space-y-0.5">
                    {change.staged_files.map((f, i) => (
                      <p key={i} className="text-xs font-mono text-muted-foreground pl-2 truncate">
                        <span className="text-muted-foreground/70 mr-1.5">{f.status}</span>{f.path}
                      </p>
                    ))}
                  </div>
                </div>
              )}

              {/* Merge conflicts */}
              {change.has_merge_conflict && change.unmerged_files.length > 0 && (
                <div className="bg-destructive/10 border border-destructive/30 rounded p-2">
                  <p className="text-xs font-medium text-destructive mb-1">
                    {t('dialogs.deleteWorkspace.mergeConflict', { count: change.unmerged_files.length })}
                  </p>
                  <div className="space-y-0.5">
                    {change.unmerged_files.map((f, i) => (
                      <p key={i} className="text-xs font-mono text-destructive pl-2 truncate">
                        <span className="text-destructive/70 mr-1.5">{f.status}</span>{f.path}
                      </p>
                    ))}
                  </div>
                </div>
              )}

              {/* Ahead of base branch */}
              {change.ahead_of_base_count > 0 && change.base_branch && (
                <div>
                  <p className="text-xs font-medium text-muted-foreground mb-1">
                    {t('dialogs.deleteWorkspace.aheadOfBase', { branch: change.base_branch, count: change.ahead_of_base_count })}
                  </p>
                  <div className="space-y-0.5">
                    {change.ahead_of_base_commits.map((c, i) => (
                      <p key={i} className="text-xs font-mono text-muted-foreground pl-2 truncate">
                        <span className="text-muted-foreground/70 mr-1.5">{c.hash.substring(0, 7)}</span>{c.message}
                      </p>
                    ))}
                  </div>
                </div>
              )}

              {/* Ahead of remote */}
              {change.ahead_count > 0 && (
                <div>
                  <p className="text-xs font-medium text-muted-foreground mb-1">
                    {t('dialogs.deleteWorkspace.aheadOfRemote', { count: change.ahead_count })}
                  </p>
                  <div className="space-y-0.5">
                    {change.ahead_commits.map((c, i) => (
                      <p key={i} className="text-xs font-mono text-muted-foreground pl-2 truncate">
                        <span className="text-muted-foreground/70 mr-1.5">{c.hash.substring(0, 7)}</span>{c.message}
                      </p>
                    ))}
                  </div>
                </div>
              )}
            </div>
          ))}
        </div>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={isDeleting}>{t('common:actions.cancel')}</AlertDialogCancel>
          <AlertDialogAction
            onClick={(e) => {
              e.preventDefault();
              onForceDelete();
            }}
            disabled={isDeleting}
            className="bg-destructive hover:bg-destructive/90"
          >
            {isDeleting ? t('common:actions.deleting') : t('dialogs.deleteWorkspace.forceDelete')}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
