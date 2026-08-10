import { useState } from 'react';
import { useQuery, useQueryClient, useMutation } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { Plus, Eye, Pencil, Copy, Trash2, Star } from 'lucide-react';
import { toast } from 'sonner';
import {
  projectBlueprintApi,
  type ProjectBlueprintSummary,
  type BlueprintColumn,
  type BlueprintScene,
} from '@/lib/project-blueprint-api';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { BlueprintEditorDialog } from '@/components/dialogs/blueprint-editor-dialog';
import { BlueprintDetailDialog } from '@/components/dialogs/blueprint-detail-dialog';

type EditorState =
  | { mode: 'create' }
  | { mode: 'edit'; id: number; name: string; description: string };

/**
 * Global project-template manager (Settings → 项目模板). Full CRUD over the
 * caller's templates + the system builtins: list, view, add, edit, duplicate,
 * set-default, delete. Builtins cannot be deleted (only duplicated to customise).
 */
export function ProjectBlueprintsSettings() {
  const { t } = useTranslation('projects');
  const qc = useQueryClient();

  const [editor, setEditor] = useState<EditorState | null>(null);
  const [editorInitialColumns, setEditorInitialColumns] = useState<BlueprintColumn[]>([]);
  const [editorInitialScenes, setEditorInitialScenes] = useState<BlueprintScene[]>([]);
  const [detailId, setDetailId] = useState<number | null>(null);
  const [deleting, setDeleting] = useState<ProjectBlueprintSummary | null>(null);

  const { data: blueprints = [], isLoading } = useQuery({
    queryKey: ['project-blueprints'],
    queryFn: () => projectBlueprintApi.list(),
  });
  const { data: defaultRes } = useQuery({
    queryKey: ['project-blueprints-default'],
    queryFn: () => projectBlueprintApi.getDefault(),
  });
  const defaultId = defaultRes?.blueprint_id ?? 0;

  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ['project-blueprints'] });
    qc.invalidateQueries({ queryKey: ['project-blueprints-default'] });
  };

  const duplicateMut = useMutation({
    mutationFn: (id: number) => projectBlueprintApi.duplicate(id),
    onSuccess: () => {
      invalidate();
      toast.success(t('tabs.settings.templates.duplicated'));
    },
    onError: (e: unknown) => toast.error(t('tabs.settings.templates.duplicateFailed', { message: e instanceof Error ? e.message : String(e) })),
  });

  const setDefaultMut = useMutation({
    mutationFn: (id: number) => projectBlueprintApi.setDefault(id),
    onSuccess: () => {
      invalidate();
      toast.success(t('tabs.settings.templates.defaultSet'));
    },
    onError: (e: unknown) => toast.error(t('tabs.settings.templates.defaultFailed', { message: e instanceof Error ? e.message : String(e) })),
  });

  const deleteMut = useMutation({
    mutationFn: (id: number) => projectBlueprintApi.remove(id),
    onSuccess: () => {
      invalidate();
      setDeleting(null);
    },
    onError: (e: unknown) => toast.error(t('tabs.settings.templates.deleteFailed', { message: e instanceof Error ? e.message : String(e) })),
  });

  const openEdit = async (bp: ProjectBlueprintSummary) => {
    // Pull full detail (columns) before opening the editor.
    try {
      const detail = await projectBlueprintApi.getDetail(bp.id);
      setEditor({ mode: 'edit', id: bp.id, name: detail.name, description: detail.description });
      setEditorInitialColumns(detail.columns);
      setEditorInitialScenes(detail.scenes);
    } catch (e) {
      toast.error(t('tabs.settings.templates.editor.loadFailed', { message: e instanceof Error ? e.message : String(e) }));
    }
  };

  return (
    <div className="py-6 space-y-6 max-w-4xl">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h2 className="text-lg font-medium">{t('tabs.settings.templates.manageTitle')}</h2>
          <p className="text-sm text-muted-foreground mt-1">{t('tabs.settings.templates.manageHint')}</p>
        </div>
        <Button
          type="button"
          onClick={() => {
            setEditorInitialColumns([]);
            setEditorInitialScenes([]);
            setEditor({ mode: 'create' });
          }}
        >
          <Plus className="h-4 w-4 mr-1" />
          {t('tabs.settings.templates.add')}
        </Button>
      </div>

      {isLoading ? (
        <p className="text-sm text-muted-foreground text-center py-6">{t('tabs.settings.templates.loading')}</p>
      ) : blueprints.length === 0 ? (
        <p className="text-sm text-muted-foreground text-center py-6">{t('tabs.settings.templates.empty')}</p>
      ) : (
        <div className="overflow-hidden rounded-md border border-warm-border">
          <table className="w-full text-sm">
            <thead className="bg-warm-muted text-warm-text-muted">
              <tr>
                <th className="text-left font-medium px-3 py-2">{t('tabs.settings.templates.colName')}</th>
                <th className="text-left font-medium px-3 py-2">{t('tabs.settings.templates.colType')}</th>
                <th className="text-left font-medium px-3 py-2">{t('tabs.settings.templates.colSize')}</th>
                <th className="text-right font-medium px-3 py-2">{t('tabs.settings.templates.colActions')}</th>
              </tr>
            </thead>
            <tbody>
              {blueprints.map((bp) => {
                const isDefault = bp.id === defaultId;
                return (
                  <tr key={bp.id} className="border-t border-warm-border align-top">
                    <td className="px-3 py-2">
                      <div className="font-medium">{bp.name}</div>
                      {bp.description ? (
                        <div className="text-xs text-muted-foreground truncate max-w-[22rem]">{bp.description}</div>
                      ) : null}
                    </td>
                    <td className="px-3 py-2">
                      <div className="flex items-center gap-1.5">
                        <Badge variant="outline" className="font-normal">
                          {bp.is_builtin ? t('tabs.settings.templates.builtin') : t('tabs.settings.templates.custom')}
                        </Badge>
                        {isDefault && (
                          <Badge variant="secondary" className="font-normal text-info">{t('tabs.settings.templates.default')}</Badge>
                        )}
                      </div>
                    </td>
                    <td className="px-3 py-2 text-muted-foreground whitespace-nowrap">
                      {t('tabs.settings.templates.counts', { columns: bp.column_count, scenes: bp.scene_count })}
                    </td>
                    <td className="px-3 py-2">
                      <div className="flex items-center justify-end gap-0.5">
                        <Button type="button" variant="ghost" size="sm" onClick={() => setDetailId(bp.id)} aria-label={t('tabs.settings.templates.view')} title={t('tabs.settings.templates.view')}>
                          <Eye className="h-4 w-4" />
                        </Button>
                        {!isDefault && (
                          <Button type="button" variant="ghost" size="sm" onClick={() => setDefaultMut.mutate(bp.id)} disabled={setDefaultMut.isPending} aria-label={t('tabs.settings.templates.setDefault')} title={t('tabs.settings.templates.setDefault')}>
                            <Star className="h-4 w-4" />
                          </Button>
                        )}
                        <Button type="button" variant="ghost" size="sm" onClick={() => duplicateMut.mutate(bp.id)} disabled={duplicateMut.isPending} aria-label={t('tabs.settings.templates.duplicate')} title={t('tabs.settings.templates.duplicate')}>
                          <Copy className="h-4 w-4" />
                        </Button>
                        {!bp.is_builtin && (
                          <Button type="button" variant="ghost" size="sm" onClick={() => openEdit(bp)} aria-label={t('tabs.settings.templates.edit')} title={t('tabs.settings.templates.edit')}>
                            <Pencil className="h-4 w-4" />
                          </Button>
                        )}
                        {!bp.is_builtin && (
                          <Button type="button" variant="ghost" size="sm" onClick={() => setDeleting(bp)} className="text-destructive hover:text-destructive/80 hover:bg-destructive/10" aria-label={t('tabs.settings.templates.delete')} title={t('tabs.settings.templates.delete')}>
                            <Trash2 className="h-4 w-4" />
                          </Button>
                        )}
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      <BlueprintEditorDialog
        open={!!editor}
        onOpenChange={(o) => { if (!o) setEditor(null); }}
        mode={editor?.mode ?? 'create'}
        initial={
          editor && editor.mode === 'edit'
            ? { id: editor.id, name: editor.name, description: editor.description, columns: editorInitialColumns, scenes: editorInitialScenes }
            : undefined
        }
        onSaved={() => setEditor(null)}
      />

      <BlueprintDetailDialog
        open={detailId !== null}
        onOpenChange={(o) => { if (!o) setDetailId(null); }}
        blueprintId={detailId}
      />

      <Dialog open={!!deleting} onOpenChange={(o) => { if (!o) setDeleting(null); }}>
        <DialogContent className="sm:max-w-[420px]">
          <DialogHeader>
            <DialogTitle>{t('tabs.settings.templates.deleteTitle')}</DialogTitle>
            <DialogDescription>
              {t('tabs.settings.templates.deleteConfirm', { name: deleting?.name ?? '' })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setDeleting(null)}>
              {t('tabs.settings.templates.editor.cancel')}
            </Button>
            <Button type="button" variant="destructive" disabled={deleteMut.isPending} onClick={() => deleting && deleteMut.mutate(deleting.id)}>
              {t('tabs.settings.templates.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
