import { useState } from 'react';
import { Link, useNavigate } from '@tanstack/react-router';
import { useTranslation } from 'react-i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowLeft } from 'lucide-react';
import { toast } from 'sonner';
import { sceneApi } from '@/lib/api';
import { Button } from '@/components/ui/button';
import { SceneEditorForm } from './components/scene-editor-form';
import type { SceneEditorSubmit } from './components/scene-editor-helpers';

interface SceneEditPageProps {
  sceneId: string;
}

export function SceneEditPage({ sceneId: sceneIdProp }: SceneEditPageProps) {
  const { t } = useTranslation('scenes');
  const navigate = useNavigate();
  const qc = useQueryClient();
  const sceneId = Number(sceneIdProp);
  const [error, setError] = useState<string | null>(null);

  const { data: scene, isLoading } = useQuery({
    queryKey: ['scene', sceneId],
    queryFn: () => sceneApi.get(sceneId),
    enabled: Number.isFinite(sceneId),
  });

  const updateMut = useMutation({
    mutationFn: (data: SceneEditorSubmit) =>
      sceneApi.update(sceneId, {
        display_name: data.displayName,
        description: data.description,
        tags: data.tags,
        definition: data.definition,
        ...(data.owner && data.owner.id > 0 ? { owner: data.owner } : {}),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['scene', sceneId] });
      qc.invalidateQueries({ queryKey: ['scenes'] });
      toast.success(t('editor.saved'));
      navigate({ to: '/scenes/$id', params: { id: String(sceneId) } });
    },
    onError: (err: unknown) => {
      setError(err instanceof Error ? err.message : String(err));
    },
  });

  if (isLoading) {
    return <div className="p-6 text-sm text-warm-text-muted">{t('list.loading')}</div>;
  }
  if (!scene) {
    return <div className="p-6 text-sm text-warm-text-muted">{t('list.empty')}</div>;
  }

  // Built-in scenes are read-only — they must be forked before editing.
  if (scene.source === 'builtin') {
    return (
      <div className="p-6 max-w-3xl mx-auto w-full space-y-4">
        <Button asChild variant="ghost" size="sm">
          <Link to="/scenes/$id" params={{ id: String(sceneId) }}>
            <ArrowLeft className="w-4 h-4 mr-1" aria-hidden />
            {t('editor.back')}
          </Link>
        </Button>
        <p className="text-sm text-warm-text-muted bg-warm-muted border border-warm-border rounded-md px-3 py-2">
          {t('detail.builtin_readonly')}
        </p>
      </div>
    );
  }

  return (
    <div className="flex flex-col h-full overflow-y-auto">
      <div className="p-6 max-w-3xl mx-auto w-full space-y-4">
        <div>
          <Button asChild variant="ghost" size="sm">
            <Link to="/scenes/$id" params={{ id: String(sceneId) }}>
              <ArrowLeft className="w-4 h-4 mr-1" aria-hidden />
              {t('editor.back')}
            </Link>
          </Button>
        </div>

        <h1 className="text-2xl font-semibold text-warm-text">{t('editor.title')}</h1>

        <SceneEditorForm
          mode="edit"
          initial={{
            slug: scene.slug,
            displayName: scene.display_name,
            description: scene.description ?? '',
            tags: scene.tags ?? [],
            definition: scene.definition,
            owner: scene.owner,
          }}
          submitting={updateMut.isPending}
          error={error}
          onSubmit={(data) => {
            setError(null);
            updateMut.mutate(data);
          }}
        />
      </div>
    </div>
  );
}
