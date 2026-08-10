import { useRef, useState } from 'react';
import { useQueryClient, useMutation } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { projectBlueprintApi } from '@/lib/project-blueprint-api';
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
import { Label } from '@/components/ui/label';

interface Props {
  projectId: number;
}

/**
 * Save-as-template entry: snapshots this project's columns + default scenes
 * into a reusable blueprint (UI: "项目模板") via a name/description dialog.
 * Full template management (list, default, delete) lives in the global
 * project-blueprints settings page, not here.
 */
export function SaveAsTemplateDialog({ projectId }: Props) {
  const { t } = useTranslation('projects');
  const qc = useQueryClient();
  const inputRef = useRef<HTMLInputElement>(null);
  const [open, setOpen] = useState(false);
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');

  const openDialog = () => {
    setName('');
    setDescription('');
    setOpen(true);
  };

  const saveMut = useMutation({
    mutationFn: () =>
      projectBlueprintApi.saveFromProject(projectId, {
        name: name.trim(),
        description: description.trim(),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['project-blueprints'] });
      toast.success(t('tabs.settings.templates.saved'));
      setOpen(false);
    },
    onError: (err: unknown) => {
      toast.error(t('tabs.settings.templates.saveFailed', { message: err instanceof Error ? err.message : String(err) }));
    },
  });

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim()) {
      inputRef.current?.focus();
      return;
    }
    saveMut.mutate();
  };

  return (
    <>
      <Button type="button" variant="outline" size="sm" onClick={openDialog} className="shrink-0">
        {t('tabs.settings.templates.saveAs')}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="sm:max-w-[440px]">
          <DialogHeader>
            <DialogTitle>{t('tabs.settings.templates.saveAs')}</DialogTitle>
            <DialogDescription>{t('tabs.settings.templates.hint')}</DialogDescription>
          </DialogHeader>
          <form onSubmit={submit} className="grid gap-4 py-2">
            <div className="grid gap-2">
              <Label htmlFor="blueprint-name">
                {t('tabs.settings.templates.nameLabel')}
                <span className="ml-1 text-destructive">*</span>
              </Label>
              <Input
                id="blueprint-name"
                ref={inputRef}
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t('tabs.settings.templates.namePlaceholder')}
                disabled={saveMut.isPending}
              />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="blueprint-desc">{t('tabs.settings.templates.descriptionLabel')}</Label>
              <Input
                id="blueprint-desc"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder={t('tabs.settings.templates.descriptionPlaceholder')}
                disabled={saveMut.isPending}
              />
            </div>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setOpen(false)}>
                {t('common:actions.cancel')}
              </Button>
              <Button type="submit" disabled={!name.trim() || saveMut.isPending}>
                {saveMut.isPending ? t('common:actions.saving') : t('common:actions.save')}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}
