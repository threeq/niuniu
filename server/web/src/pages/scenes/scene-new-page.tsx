import { useState } from 'react';
import { Link, useNavigate } from '@tanstack/react-router';
import { useTranslation } from 'react-i18next';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { ArrowLeft } from 'lucide-react';
import { toast } from 'sonner';
import { sceneApi } from '@/lib/api';
import { Button } from '@/components/ui/button';
import { SceneEditorForm } from './components/scene-editor-form';
import type { SceneEditorSubmit } from './components/scene-editor-helpers';

export function SceneNewPage() {
  const { t } = useTranslation('scenes');
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [error, setError] = useState<string | null>(null);

  const createMut = useMutation({
    mutationFn: (data: SceneEditorSubmit) =>
      sceneApi.create({
        slug: data.slug,
        display_name: data.displayName,
        description: data.description,
        tags: data.tags,
        definition: data.definition,
        ...(data.owner && data.owner.id > 0 ? { owner: data.owner } : {}),
      }),
    onSuccess: (scene) => {
      qc.invalidateQueries({ queryKey: ['scenes'] });
      toast.success(t('new.created'));
      navigate({ to: '/scenes/$id', params: { id: String(scene.id) } });
    },
    onError: (err: unknown) => {
      setError(err instanceof Error ? err.message : String(err));
    },
  });

  return (
    <div className="flex flex-col h-full overflow-y-auto">
      <div className="p-6 max-w-3xl mx-auto w-full space-y-4">
        <div>
          <Button asChild variant="ghost" size="sm">
            <Link to="/scenes">
              <ArrowLeft className="w-4 h-4 mr-1" aria-hidden />
              {t('new.back')}
            </Link>
          </Button>
        </div>

        <h1 className="text-2xl font-semibold text-warm-text">{t('new.title')}</h1>

        <SceneEditorForm
          mode="create"
          submitting={createMut.isPending}
          error={error}
          onSubmit={(data) => {
            setError(null);
            createMut.mutate(data);
          }}
        />
      </div>
    </div>
  );
}
