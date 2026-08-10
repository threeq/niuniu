import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import {
  Dialog,
  DialogContent,
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
import { labels as api } from '@/lib/api';
import type { Label } from '@/types/api';
import { ColorPicker } from '@/components/issue/color-picker';
import { contrastTextColor } from '@/lib/label-color';

type Props = { projectId: number; canManage: boolean };

/**
 * Project-level shared label dictionary management.
 *
 * - Read path is open to anyone who can see the project; controls render
 *   only when `canManage` is true (owner of personal project, or
 *   owner/admin of an org-owned project).
 * - Uses the `labels-with-usage` query key so the badge in the issue
 *   picker stays untouched (that one fetches without `with_usage`).
 */
export function LabelsSection({ projectId, canManage }: Props) {
  const { t } = useTranslation('projects');
  const qc = useQueryClient();
  const { data: items = [] } = useQuery<Label[]>({
    queryKey: ['labels-with-usage', projectId],
    queryFn: () => api.list(projectId, undefined, true),
  });

  const [editing, setEditing] = useState<Label | 'new' | null>(null);
  const [deleting, setDeleting] = useState<Label | null>(null);
  const close = () => setEditing(null);
  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ['labels-with-usage', projectId] });
    qc.invalidateQueries({ queryKey: ['labels', projectId] });
  };

  // onError surfaces 4xx/5xx as a toast — without it, server failures (e.g.
  // 403 from a stale role, 500 on a DB hiccup) leave the user staring at a
  // dialog that won't close. The labels.create wrapper passes
  // `suppressError: true` to apiFetch so the global "API Error" toast is
  // suppressed; here we substitute a dedicated message.
  const onMutationError = (err: unknown) => {
    toast.error(t('common:status.failed'));
    console.error('label mutation failed', err);
  };

  const createMut = useMutation({
    mutationFn: (b: { name: string; color: string; description: string }) =>
      api.create(projectId, b),
    onSuccess: () => {
      invalidate();
      close();
    },
    onError: onMutationError,
  });
  const updateMut = useMutation({
    mutationFn: ({
      id,
      ...b
    }: { id: number; name?: string; color?: string; description?: string }) =>
      api.update(projectId, id, b),
    onSuccess: () => {
      invalidate();
      close();
    },
    onError: onMutationError,
  });
  const deleteMut = useMutation({
    mutationFn: (id: number) => api.delete(projectId, id),
    onSuccess: invalidate,
    onError: onMutationError,
  });

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold">
          {t('settings.labels.title')}
        </h3>
        {canManage && (
          <button
            type="button"
            onClick={() => setEditing('new')}
            className="text-xs text-primary hover:underline"
          >
            + {t('settings.labels.create')}
          </button>
        )}
      </div>
      <table className="w-full text-sm">
        <thead className="text-xs text-muted-foreground">
          <tr>
            <th className="text-left py-1">
              {t('settings.labels.col.label')}
            </th>
            <th className="text-left py-1">
              {t('settings.labels.col.description')}
            </th>
            <th className="text-right py-1">
              {t('settings.labels.col.usage')}
            </th>
            {canManage && <th />}
          </tr>
        </thead>
        <tbody>
          {items.length === 0 && (
            <tr>
              <td
                colSpan={canManage ? 4 : 3}
                className="py-4 text-center text-muted-foreground"
              >
                {t('settings.labels.empty')}
              </td>
            </tr>
          )}
          {items.map((l) => (
            <tr key={l.id} className="border-t border-border">
              <td className="py-1.5">
                <span
                  className="rounded px-1.5 py-0.5 text-[11px]"
                  style={{
                    backgroundColor: l.color,
                    color: contrastTextColor(l.color),
                  }}
                >
                  {l.name}
                </span>
              </td>
              <td className="py-1.5 text-muted-foreground">
                {l.description || '—'}
              </td>
              <td className="py-1.5 text-right">{l.usage_count ?? 0}</td>
              {canManage && (
                <td className="py-1.5 text-right">
                  <button
                    type="button"
                    onClick={() => setEditing(l)}
                    className="text-xs text-muted-foreground hover:text-foreground"
                  >
                    {t('common:actions.edit', 'Edit')}
                  </button>
                  <button
                    type="button"
                    onClick={() => setDeleting(l)}
                    className="text-xs text-destructive ml-2 hover:underline"
                  >
                    {t('common:actions.delete', 'Delete')}
                  </button>
                </td>
              )}
            </tr>
          ))}
        </tbody>
      </table>

      <Dialog
        open={editing !== null}
        onOpenChange={(o) => {
          if (!o) close();
        }}
      >
        <DialogContent className="space-y-3">
          <DialogTitle>
            {editing === 'new'
              ? t('settings.labels.create')
              : t('common:actions.edit', 'Edit')}
          </DialogTitle>
          {editing && (
            <LabelForm
              initial={
                editing === 'new'
                  ? { name: '', color: '#d73a4a', description: '' }
                  : {
                      name: editing.name,
                      color: editing.color,
                      description: editing.description,
                    }
              }
              onSubmit={(v) =>
                editing === 'new'
                  ? createMut.mutate(v)
                  : updateMut.mutate({ id: (editing as Label).id, ...v })
              }
              onCancel={close}
            />
          )}
        </DialogContent>
      </Dialog>

      <AlertDialog
        open={!!deleting}
        onOpenChange={(o) => {
          if (!o) setDeleting(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('common:actions.delete', 'Delete')}</AlertDialogTitle>
            <AlertDialogDescription>
              {t('settings.labels.confirmDelete', {
                count: deleting?.usage_count ?? 0,
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('common:actions.cancel', 'Cancel')}</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (deleting) deleteMut.mutate(deleting.id);
                setDeleting(null);
              }}
              className="bg-destructive hover:bg-destructive/90 text-destructive-foreground"
            >
              {t('common:actions.delete', 'Delete')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function LabelForm({
  initial,
  onSubmit,
  onCancel,
}: {
  initial: { name: string; color: string; description: string };
  onSubmit: (v: { name: string; color: string; description: string }) => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation('projects');
  const [name, setName] = useState(initial.name);
  const [color, setColor] = useState(initial.color);
  const [description, setDescription] = useState(initial.description);
  return (
    <div className="space-y-2">
      <input
        value={name}
        onChange={(e) => setName(e.target.value)}
        maxLength={50}
        placeholder={t('settings.labels.placeholder.name')}
        className="w-full bg-background border border-border rounded px-2 py-1 text-sm"
      />
      <input
        value={description}
        onChange={(e) => setDescription(e.target.value)}
        placeholder={t('settings.labels.placeholder.description')}
        className="w-full bg-background border border-border rounded px-2 py-1 text-sm"
      />
      <ColorPicker value={color} onChange={setColor} />
      <div className="flex gap-2 justify-end">
        <button
          type="button"
          onClick={onCancel}
          className="rounded px-3 py-1 text-xs border border-border"
        >
          {t('common:actions.cancel', 'Cancel')}
        </button>
        <button
          type="button"
          onClick={() => onSubmit({ name, color, description })}
          disabled={!name.trim()}
          className="bg-primary text-primary-foreground rounded px-3 py-1 text-xs disabled:opacity-50"
        >
          {t('common:actions.save', 'Save')}
        </button>
      </div>
    </div>
  );
}
