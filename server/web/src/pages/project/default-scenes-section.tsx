import { useState } from 'react';
import { useQuery, useQueryClient, useMutation } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { Plus, X } from 'lucide-react';
import { toast } from 'sonner';
import { projectSceneApi, sceneApi } from '@/lib/api';
import type { ProjectDefaultScene, Scene } from '@/types/api';
import { Button } from '@/components/ui/button';

interface Props {
  projectId: number;
  canManage: boolean;
}

/**
 * Project default scenes: an ordered list of scenes auto-attached to every new
 * workspace created under this project (issue -> column -> project -> defaults).
 * Backend prefill already exists in WorkspaceService.Create; this is the missing
 * management surface. Stacking is union-deduped at projection time, so adding
 * scenes that share an MCP/agent does not duplicate resources.
 */
export function DefaultScenesSection({ projectId, canManage }: Props) {
  const { t } = useTranslation('projects');
  const qc = useQueryClient();
  const [pickSceneId, setPickSceneId] = useState<number | ''>('');

  const { data: defaults = [], isLoading } = useQuery({
    queryKey: ['project-default-scenes', String(projectId)],
    queryFn: () => projectSceneApi.listDefaults(projectId),
  });

  const { data: scenes = [] } = useQuery({
    queryKey: ['scenes'],
    queryFn: () => sceneApi.list(),
  });

  const invalidate = () =>
    qc.invalidateQueries({ queryKey: ['project-default-scenes', String(projectId)] });

  const attachMut = useMutation({
    mutationFn: (sceneId: number) => projectSceneApi.attach(projectId, sceneId),
    onSuccess: () => {
      invalidate();
      setPickSceneId('');
    },
    onError: (err: unknown) => {
      const msg = err instanceof Error ? err.message : String(err);
      toast.error(t('tabs.settings.defaultScenes.addFailed', { message: msg }));
    },
  });

  const detachMut = useMutation({
    mutationFn: (sceneId: number) => projectSceneApi.detach(projectId, sceneId),
    onSuccess: invalidate,
    onError: (err: unknown) => {
      const msg = err instanceof Error ? err.message : String(err);
      toast.error(t('tabs.settings.defaultScenes.removeFailed', { message: msg }));
    },
  });

  const addedIds = new Set(defaults.map((d: ProjectDefaultScene) => d.scene_id));
  const available = (scenes as Scene[]).filter((s) => !addedIds.has(s.id));

  const handleAdd = () => {
    if (!pickSceneId) return;
    attachMut.mutate(pickSceneId as number);
  };

  return (
    <div className="border rounded-lg p-4 space-y-4">
      <h3 className="text-sm font-semibold">{t('tabs.settings.defaultScenes.title')}</h3>
      <p className="text-xs text-muted-foreground">{t('tabs.settings.defaultScenes.hint')}</p>

      {isLoading ? (
        <p className="text-xs text-muted-foreground text-center py-2">
          {t('tabs.settings.defaultScenes.loading')}
        </p>
      ) : defaults.length > 0 ? (
        <div className="border rounded-md divide-y">
          {defaults.map((d: ProjectDefaultScene) => (
            <div key={d.scene_id} className="flex items-center gap-2 p-2">
              <div className="flex-1 min-w-0">
                <p className="text-sm font-medium truncate">{d.display_name}</p>
                <p className="text-xs text-muted-foreground truncate">{d.slug}</p>
              </div>
              <span className="text-xs text-muted-foreground shrink-0">
                {t(`tabs.settings.defaultScenes.source.${d.source}`)}
              </span>
              {canManage && (
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  onClick={() => detachMut.mutate(d.scene_id)}
                  disabled={detachMut.isPending}
                  className="text-destructive hover:text-destructive/80 hover:bg-destructive/10"
                  aria-label={t('tabs.settings.defaultScenes.remove')}
                >
                  <X className="h-4 w-4" />
                </Button>
              )}
            </div>
          ))}
        </div>
      ) : (
        <p className="text-xs text-muted-foreground text-center py-2">
          {t('tabs.settings.defaultScenes.empty')}
        </p>
      )}

      {canManage && (
        <div className="flex gap-2">
          <select
            value={pickSceneId}
            onChange={(e) => setPickSceneId(e.target.value ? Number(e.target.value) : '')}
            disabled={attachMut.isPending || available.length === 0}
            className="flex-1 h-9 rounded-md border border-input bg-background px-3 py-1 text-sm"
          >
            <option value="">
              {available.length === 0
                ? t('tabs.settings.defaultScenes.allAdded')
                : t('tabs.settings.defaultScenes.selectScene')}
            </option>
            {available.map((s) => (
              <option key={s.id} value={s.id}>
                {s.display_name}
              </option>
            ))}
          </select>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={handleAdd}
            disabled={!pickSceneId || attachMut.isPending}
          >
            <Plus className="h-4 w-4 mr-1" /> {t('tabs.settings.defaultScenes.add')}
          </Button>
        </div>
      )}
    </div>
  );
}
