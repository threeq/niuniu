import { useState, useMemo } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { Search, Star } from 'lucide-react';
import { sceneApi, workspaceSceneApi } from '@/lib/api';
import type { Scene, RankedScene } from '@/types/api';
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';

interface WorkspaceSceneAttachDialogProps {
  workspaceId: number;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Optional set of scene ids that are already attached; hidden from results. */
  attachedSceneIds?: number[];
}

function SceneRow({
  scene,
  score,
  hitsCount,
  onAttach,
  isAttaching,
}: {
  scene: Scene;
  score?: number;
  hitsCount?: number;
  onAttach: (scene: Scene) => void;
  isAttaching: boolean;
}) {
  const { t } = useTranslation('scenes');
  return (
    <div className="flex items-start gap-3 border border-warm-border rounded-md p-3 bg-warm-surface">
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2 flex-wrap">
          <span className="text-sm font-medium text-warm-text truncate">
            {scene.display_name}
          </span>
          <Badge
            variant={scene.source === 'builtin' ? 'secondary' : 'default'}
            className="text-[11px]"
          >
            {t(`source.${scene.source}`)}
          </Badge>
          {typeof score === 'number' && (
            <span className="text-xs text-warm-text-muted inline-flex items-center gap-1">
              <Star className="w-3 h-3" aria-hidden />
              {t('attach_dialog.score', { score })}
            </span>
          )}
          {typeof hitsCount === 'number' && hitsCount > 0 && (
            <span className="text-xs text-warm-text-muted">
              {t('attach_dialog.hit_rules', { count: hitsCount })}
            </span>
          )}
        </div>
        <div className="text-xs text-warm-text-muted font-mono truncate mt-0.5">
          {scene.slug}
        </div>
        {scene.description && (
          <p className="text-xs text-warm-text-muted line-clamp-2 mt-1">
            {scene.description}
          </p>
        )}
      </div>
      <Button
        type="button"
        size="sm"
        onClick={() => onAttach(scene)}
        disabled={isAttaching}
      >
        {isAttaching ? t('attach_dialog.attaching') : t('attach_dialog.attach')}
      </Button>
    </div>
  );
}

export function WorkspaceSceneAttachDialog({
  workspaceId,
  open,
  onOpenChange,
  attachedSceneIds = [],
}: WorkspaceSceneAttachDialogProps) {
  const { t } = useTranslation('scenes');
  const qc = useQueryClient();
  const [search, setSearch] = useState('');

  const { data: scenes = [], isLoading: scenesLoading } = useQuery({
    queryKey: ['scenes'],
    queryFn: () => sceneApi.list(),
    enabled: open,
  });

  const { data: ranked = [], isLoading: ranksLoading } = useQuery({
    queryKey: ['workspace-scene-recommendations', workspaceId],
    queryFn: () => workspaceSceneApi.recommendations(workspaceId),
    enabled: open,
  });

  const attachMut = useMutation({
    mutationFn: (sceneId: number) => workspaceSceneApi.attach(workspaceId, sceneId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['workspace-scene-layers', workspaceId] });
      qc.invalidateQueries({ queryKey: ['workspace-projection', workspaceId] });
      qc.invalidateQueries({ queryKey: ['workspace-scene-recommendations', workspaceId] });
      toast.success(t('attach_dialog.attach'));
      onOpenChange(false);
    },
  });

  const attachedSet = useMemo(
    () => new Set(attachedSceneIds),
    [attachedSceneIds],
  );

  const allFiltered = useMemo(() => {
    const q = search.trim().toLowerCase();
    return scenes.filter((s) => {
      if (attachedSet.has(s.id)) return false;
      if (
        q &&
        !s.display_name.toLowerCase().includes(q) &&
        !s.slug.toLowerCase().includes(q)
      ) {
        return false;
      }
      return true;
    });
  }, [scenes, attachedSet, search]);

  const recommendedFiltered = useMemo(() => {
    return ranked.filter((r: RankedScene) => !attachedSet.has(r.scene.id));
  }, [ranked, attachedSet]);

  function handleAttach(scene: Scene) {
    attachMut.mutate(scene.id);
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[640px] max-h-[80vh]">
        <DialogHeader>
          <DialogTitle>{t('attach_dialog.title')}</DialogTitle>
        </DialogHeader>

        <Tabs defaultValue="recommendations" className="w-full">
          <TabsList>
            <TabsTrigger value="recommendations">
              {t('attach_dialog.tab_recommendations')}
            </TabsTrigger>
            <TabsTrigger value="all">{t('attach_dialog.tab_all')}</TabsTrigger>
          </TabsList>

          <TabsContent value="recommendations" className="space-y-2">
            {ranksLoading ? (
              <p className="text-xs text-warm-text-muted py-4 text-center">
                {t('workspace_panel.loading')}
              </p>
            ) : recommendedFiltered.length === 0 ? (
              <p className="text-xs text-warm-text-muted py-4 text-center">
                {t('attach_dialog.no_recommendations')}
              </p>
            ) : (
              recommendedFiltered.map((r) => (
                <SceneRow
                  key={r.scene.id}
                  scene={r.scene}
                  score={r.score}
                  hitsCount={r.hits?.length ?? 0}
                  onAttach={handleAttach}
                  isAttaching={
                    attachMut.isPending && attachMut.variables === r.scene.id
                  }
                />
              ))
            )}
          </TabsContent>

          <TabsContent value="all" className="space-y-2">
            <div className="relative">
              <Search
                className="absolute left-2.5 top-1/2 -translate-y-1/2 w-4 h-4 text-warm-text-muted"
                aria-hidden
              />
              <Input
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder={t('attach_dialog.search_placeholder')}
                className="pl-8"
              />
            </div>
            {scenesLoading ? (
              <p className="text-xs text-warm-text-muted py-4 text-center">
                {t('workspace_panel.loading')}
              </p>
            ) : allFiltered.length === 0 ? (
              <p className="text-xs text-warm-text-muted py-4 text-center">
                {t('attach_dialog.no_scenes')}
              </p>
            ) : (
              allFiltered.map((s) => (
                <SceneRow
                  key={s.id}
                  scene={s}
                  onAttach={handleAttach}
                  isAttaching={attachMut.isPending && attachMut.variables === s.id}
                />
              ))
            )}
          </TabsContent>
        </Tabs>

        <DialogFooter>
          <Button
            type="button"
            variant="ghost"
            onClick={() => onOpenChange(false)}
          >
            {t('attach_dialog.close')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
