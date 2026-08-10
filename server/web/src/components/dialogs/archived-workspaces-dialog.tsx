import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from '@tanstack/react-router';
import { Archive, GitBranch, Trash2, AlertCircle, Eye } from 'lucide-react';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
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
import { useArchivedWorkspaces } from '@/lib/hooks/use-workspaces';
import { WORKSPACE_STATUS_LABELS } from '@/lib/workspace-status';

const statusBadge: Record<string, { bg: string; text: string }> = {
  created: { bg: 'bg-muted', text: 'text-muted-foreground' },
  running: { bg: 'bg-emerald-500 dark:bg-emerald-400', text: 'text-white dark:text-neutral-950' },
  needs_review: { bg: 'bg-amber-500 dark:bg-amber-400', text: 'text-neutral-950' },
  attention: { bg: 'bg-red-500 dark:bg-red-400', text: 'text-white' },
  completed: { bg: 'bg-blue-500 dark:bg-blue-400', text: 'text-white' },
};

interface ArchivedWorkspacesDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export function ArchivedWorkspacesDialog({ open, onOpenChange }: ArchivedWorkspacesDialogProps) {
  const { t } = useTranslation('workspaces');
  const { archivedWorkspaces, isLoading, deleteArchived, isDeleting } = useArchivedWorkspaces();
  const [confirmDeleteId, setConfirmDeleteId] = useState<string | null>(null);

  const handleDelete = async () => {
    if (!confirmDeleteId) return;
    try {
      await deleteArchived(confirmDeleteId);
    } finally {
      setConfirmDeleteId(null);
    }
  };

  const formatDate = (dateStr: string | null) => {
    if (!dateStr) return '';
    return new Date(dateStr).toLocaleString('zh-CN', {
      year: 'numeric', month: '2-digit', day: '2-digit',
      hour: '2-digit', minute: '2-digit',
    });
  };

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent className="max-w-xl max-h-[70vh]">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <Archive className="w-4 h-4" />
              {t('dialogs.archived.title')}
            </DialogTitle>
            {archivedWorkspaces && (
              <p className="text-xs text-muted-foreground">{t('dialogs.archived.count', { count: archivedWorkspaces.length })}</p>
            )}
          </DialogHeader>

          <div className="space-y-1 mt-2">
            {isLoading ? (
              <div className="py-8 text-center text-sm text-muted-foreground">{t('dialogs.archived.loading')}</div>
            ) : !archivedWorkspaces || archivedWorkspaces.length === 0 ? (
              <div className="py-8 text-center text-sm text-muted-foreground">{t('dialogs.archived.empty')}</div>
            ) : (
              archivedWorkspaces.map((ws) => {
                const badge = statusBadge[ws.status] ?? statusBadge.created;
                const label = WORKSPACE_STATUS_LABELS[ws.status as keyof typeof WORKSPACE_STATUS_LABELS] ?? ws.status;
                return (
                  <div key={ws.id} className="p-3 border rounded-md">
                    <div className="flex items-start justify-between gap-3">
                      <div className="flex-1 min-w-0 space-y-1">
                        <div className="flex items-center gap-2 flex-wrap">
                          <span className="text-[11px] text-muted-foreground font-mono">!{ws.id}</span>
                          <span className="text-sm font-medium truncate">{ws.name}</span>
                          <span className={`text-[10px] px-1.5 py-0.5 rounded-full ${badge.bg} ${badge.text}`}>
                            {label}
                          </span>
                        </div>

                        {ws.issue_title && (
                          <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
                            <AlertCircle className="w-3 h-3 shrink-0" />
                            <span className="truncate">{ws.issue_title}</span>
                            {ws.project_name && (
                              <span className="text-[11px]">· {ws.project_name}</span>
                            )}
                          </div>
                        )}

                        {ws.worktrees.map((wt, idx) => (
                          <div key={idx} className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
                            <GitBranch className="w-3 h-3 shrink-0" />
                            <span>{wt.repo_name}</span>
                            <span className="font-mono text-foreground/70">{wt.branch}</span>
                            <span>← {wt.base_branch}</span>
                          </div>
                        ))}

                        <div className="text-[11px] text-muted-foreground">
                          {t('dialogs.archived.archivedAt', { date: formatDate(ws.archived_at), created: formatDate(ws.created_at) })}
                        </div>
                      </div>

                      <div className="flex items-center gap-1 shrink-0">
                        <Link
                          to="/workspaces/$id"
                          params={{ id: ws.id }}
                          className="p-1 text-muted-foreground hover:text-foreground rounded hover:bg-accent"
                          title={t('dialogs.archived.viewArchived')}
                          onClick={() => onOpenChange(false)}
                        >
                          <Eye className="w-3.5 h-3.5" />
                        </Link>
                        <button
                          onClick={() => setConfirmDeleteId(ws.id)}
                          disabled={isDeleting}
                          className="px-2 py-1 text-xs border border-destructive/30 bg-destructive/10 text-destructive rounded-md hover:bg-destructive/20 disabled:opacity-50"
                        >
                          <Trash2 className="w-3 h-3" />
                        </button>
                      </div>
                    </div>
                  </div>
                );
              })
            )}
          </div>

          <p className="text-[11px] text-muted-foreground text-center mt-3">
            {t('dialogs.archived.footer')}
          </p>
        </DialogContent>
      </Dialog>

      <AlertDialog open={confirmDeleteId !== null} onOpenChange={(open) => { if (!open) setConfirmDeleteId(null); }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('dialogs.archived.confirmTitle')}</AlertDialogTitle>
            <AlertDialogDescription>
              {t('dialogs.archived.confirmDescription')}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('common:actions.cancel')}</AlertDialogCancel>
            <AlertDialogAction onClick={handleDelete} className="bg-destructive hover:bg-destructive/90">
              {isDeleting ? t('common:actions.deleting') : t('dialogs.archived.confirmDelete')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
