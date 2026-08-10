import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { Plus, ArrowUp, ArrowDown, Trash2, X } from 'lucide-react';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/components/ui/textarea';
import { Button } from '@/components/ui/button';
import { Label } from '@/components/ui/label';
import { Badge } from '@/components/ui/badge';
import { sceneApi } from '@/lib/api';
import type { Scene } from '@/types/api';
import { projectBlueprintApi, type BlueprintColumn, type BlueprintScene } from '@/lib/project-blueprint-api';

const PRIMITIVES = ['none', 'instruct', 'complete'] as const;

// A column being edited (position is derived from array order on save).
type EditCol = {
  name: string;
  op_primitive: string;
  op_instruction: string;
  when_to_use: string;
  lifecycle_mapping: string;
};

function blankColumn(): EditCol {
  return { name: '', op_primitive: 'none', op_instruction: '', when_to_use: '', lifecycle_mapping: '' };
}

function toEditCols(cols: BlueprintColumn[]): EditCol[] {
  return cols.map((c) => ({
    name: c.name,
    op_primitive: c.op_primitive || 'none',
    op_instruction: c.op_instruction || '',
    when_to_use: c.when_to_use || '',
    lifecycle_mapping: c.lifecycle_mapping || '',
  }));
}

interface Props {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  mode: 'create' | 'edit';
  initial?: { id?: number; name: string; description: string; columns: BlueprintColumn[]; scenes?: BlueprintScene[] };
  owner?: { type: string; id: number };
  onSaved?: () => void;
}

/**
 * Create / edit a project template: name, description, and an ordered column
 * editor (each column = name + op_primitive + when_to_use + op_instruction).
 * Scenes are preserved on edit (managed via "save from project"), not edited here.
 */
export function BlueprintEditorDialog({ open, onOpenChange, mode, initial, owner, onSaved }: Props) {
  const { t } = useTranslation('projects');
  const qc = useQueryClient();

  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [columns, setColumns] = useState<EditCol[]>([blankColumn()]);
  const [scenes, setScenes] = useState<BlueprintScene[]>([]);
  const [pickScene, setPickScene] = useState('');

  // Available scenes for the picker (caller's scenes + builtins).
  const { data: availableScenes = [] } = useQuery({
    queryKey: ['scenes'],
    queryFn: () => sceneApi.list(),
    enabled: open,
  });

  // Re-seed the form when the dialog opens for a different target. Compare during
  // render (same pattern as ColumnExtensionDialog) — keyed on open + initial id.
  const targetKey = `${open ? 1 : 0}:${initial?.id ?? 'new'}`;
  const [seenKey, setSeenKey] = useState('');
  if (open && seenKey !== targetKey) {
    setSeenKey(targetKey);
    setName(initial?.name ?? '');
    setDescription(initial?.description ?? '');
    setColumns(initial && initial.columns.length ? toEditCols(initial.columns) : [blankColumn()]);
    setScenes(initial?.scenes ?? []);
    setPickScene('');
  }

  const addScene = (slug: string) => {
    const sc = (availableScenes as Scene[]).find((s) => s.slug === slug);
    if (!sc || scenes.some((x) => x.slug === sc.slug)) return;
    setScenes((cur) => [...cur, { slug: sc.slug, display_name: sc.display_name, source: sc.source }]);
    setPickScene('');
  };
  const removeScene = (slug: string) => setScenes((cur) => cur.filter((s) => s.slug !== slug));
  const sceneCandidates = (availableScenes as Scene[]).filter((s) => !scenes.some((x) => x.slug === s.slug));

  const setCol = (i: number, patch: Partial<EditCol>) =>
    setColumns((cs) => cs.map((c, idx) => (idx === i ? { ...c, ...patch } : c)));
  const addCol = () => setColumns((cs) => [...cs, blankColumn()]);
  const removeCol = (i: number) => setColumns((cs) => cs.filter((_, idx) => idx !== i));
  const moveCol = (i: number, dir: -1 | 1) =>
    setColumns((cs) => {
      const j = i + dir;
      if (j < 0 || j >= cs.length) return cs;
      const next = [...cs];
      [next[i], next[j]] = [next[j], next[i]];
      return next;
    });

  const mut = useMutation({
    mutationFn: () => {
      const payloadCols: BlueprintColumn[] = columns.map((c, i) => ({
        name: c.name.trim(),
        position: i,
        op_primitive: c.op_primitive,
        op_instruction: c.op_primitive === 'instruct' ? c.op_instruction.trim() : '',
        when_to_use: c.when_to_use.trim(),
        lifecycle_mapping: c.lifecycle_mapping,
      }));
      const body = { name: name.trim(), description: description.trim(), columns: payloadCols, scenes };
      if (mode === 'edit' && initial?.id) {
        return projectBlueprintApi.update(initial.id, body);
      }
      return projectBlueprintApi.create({ ...body, owner });
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['project-blueprints'] });
      toast.success(t('tabs.settings.templates.editor.saved'));
      onSaved?.();
      onOpenChange(false);
    },
    onError: (e: unknown) => {
      toast.error(t('tabs.settings.templates.editor.saveFailed', { message: e instanceof Error ? e.message : String(e) }));
    },
  });

  const validColumns = columns.filter((c) => c.name.trim()).length;
  const canSave = name.trim().length > 0 && validColumns > 0 && columns.every((c) => c.name.trim()) && !mut.isPending;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[640px] max-h-[85vh]">
        <DialogHeader>
          <DialogTitle>
            {mode === 'edit'
              ? t('tabs.settings.templates.editor.titleEdit')
              : t('tabs.settings.templates.editor.titleCreate')}
          </DialogTitle>
          <DialogDescription>{t('tabs.settings.templates.editor.description')}</DialogDescription>
        </DialogHeader>

        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (canSave) mut.mutate();
          }}
          className="grid gap-4 py-2"
        >
          <div className="grid gap-2">
            <Label htmlFor="bp-name">
              {t('tabs.settings.templates.nameLabel')} <span className="text-destructive">*</span>
            </Label>
            <Input id="bp-name" value={name} onChange={(e) => setName(e.target.value)} placeholder={t('tabs.settings.templates.namePlaceholder')} />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="bp-desc">{t('tabs.settings.templates.descriptionLabel')}</Label>
            <Input id="bp-desc" value={description} onChange={(e) => setDescription(e.target.value)} placeholder={t('tabs.settings.templates.descriptionPlaceholder')} />
          </div>

          <div className="grid gap-2">
            <div className="flex items-center justify-between">
              <Label>{t('tabs.settings.templates.editor.columns')}</Label>
              <Button type="button" variant="outline" size="sm" onClick={addCol}>
                <Plus className="h-3.5 w-3.5 mr-1" />
                {t('tabs.settings.templates.editor.addColumn')}
              </Button>
            </div>

            <div className="space-y-3">
              {columns.map((c, i) => (
                <div key={i} className="rounded-md border border-warm-border p-3 space-y-2">
                  <div className="flex items-center gap-2">
                    <span className="text-xs text-muted-foreground w-5 shrink-0">{i + 1}</span>
                    <Input
                      value={c.name}
                      onChange={(e) => setCol(i, { name: e.target.value })}
                      placeholder={t('tabs.settings.templates.editor.columnNamePlaceholder')}
                      className="flex-1"
                    />
                    <select
                      value={c.op_primitive}
                      onChange={(e) => setCol(i, { op_primitive: e.target.value })}
                      className="h-9 rounded-md border border-warm-border bg-warm-surface px-2 text-sm"
                      aria-label={t('tabs.settings.columns.opPrimitive')}
                    >
                      {PRIMITIVES.map((p) => (
                        <option key={p} value={p}>
                          {t(`tabs.settings.columns.opPrimitiveOption.${p}`)}
                        </option>
                      ))}
                    </select>
                    <Button type="button" variant="ghost" size="sm" onClick={() => moveCol(i, -1)} disabled={i === 0} aria-label={t('tabs.settings.templates.editor.moveUp')}>
                      <ArrowUp className="h-4 w-4" />
                    </Button>
                    <Button type="button" variant="ghost" size="sm" onClick={() => moveCol(i, 1)} disabled={i === columns.length - 1} aria-label={t('tabs.settings.templates.editor.moveDown')}>
                      <ArrowDown className="h-4 w-4" />
                    </Button>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      onClick={() => removeCol(i)}
                      disabled={columns.length === 1}
                      className="text-destructive hover:text-destructive/80 hover:bg-destructive/10"
                      aria-label={t('tabs.settings.templates.editor.removeColumn')}
                    >
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </div>
                  <Input
                    value={c.when_to_use}
                    onChange={(e) => setCol(i, { when_to_use: e.target.value })}
                    placeholder={t('tabs.settings.columns.whenToUse')}
                  />
                  {c.op_primitive === 'instruct' && (
                    <Textarea
                      rows={2}
                      value={c.op_instruction}
                      onChange={(e) => setCol(i, { op_instruction: e.target.value })}
                      placeholder={t('tabs.settings.columns.opInstruction')}
                    />
                  )}
                </div>
              ))}
            </div>
          </div>

          <div className="grid gap-2">
            <Label>{t('tabs.settings.templates.editor.scenes')}</Label>
            <p className="text-xs text-muted-foreground">{t('tabs.settings.templates.editor.scenesHint')}</p>
            {scenes.length > 0 ? (
              <div className="flex flex-wrap gap-1.5">
                {scenes.map((s) => (
                  <Badge key={s.slug} variant="secondary" className="font-normal gap-1 pr-1">
                    {s.display_name || s.slug}
                    <button
                      type="button"
                      onClick={() => removeScene(s.slug)}
                      className="rounded-sm hover:bg-destructive/10 hover:text-destructive"
                      aria-label={t('tabs.settings.templates.editor.removeScene')}
                    >
                      <X className="h-3 w-3" />
                    </button>
                  </Badge>
                ))}
              </div>
            ) : (
              <p className="text-xs text-muted-foreground">{t('tabs.settings.templates.editor.noScenes')}</p>
            )}
            <select
              value={pickScene}
              onChange={(e) => { if (e.target.value) addScene(e.target.value); }}
              disabled={sceneCandidates.length === 0}
              className="h-9 rounded-md border border-warm-border bg-warm-surface px-2 text-sm"
            >
              <option value="">
                {sceneCandidates.length === 0
                  ? t('tabs.settings.templates.editor.allScenesAdded')
                  : t('tabs.settings.templates.editor.addScene')}
              </option>
              {sceneCandidates.map((s) => (
                <option key={s.id} value={s.slug}>{s.display_name}</option>
              ))}
            </select>
          </div>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t('tabs.settings.templates.editor.cancel')}
            </Button>
            <Button type="submit" disabled={!canSave}>
              {mut.isPending ? t('tabs.settings.templates.editor.saving') : t('tabs.settings.templates.editor.save')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
