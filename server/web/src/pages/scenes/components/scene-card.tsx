import { Link } from '@tanstack/react-router';
import { useTranslation } from 'react-i18next';
import { Layers } from 'lucide-react';
import type { Scene } from '@/types/api';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';

interface SceneCardProps {
  scene: Scene;
  onAttach?: (scene: Scene) => void;
}

const MAX_VISIBLE_TAGS = 3;

export function SceneCard({ scene, onAttach }: SceneCardProps) {
  const { t } = useTranslation('scenes');
  const tags = scene.tags ?? [];
  const visibleTags = tags.slice(0, MAX_VISIBLE_TAGS);
  const hiddenTagsCount = tags.length - visibleTags.length;

  const sourceVariant: 'default' | 'secondary' | 'outline' =
    scene.source === 'builtin'
      ? 'secondary'
      : scene.source === 'user'
        ? 'default'
        : 'outline';

  return (
    <div
      className="bg-warm-surface border border-warm-border rounded-lg shadow-sm p-4
                 hover:shadow-md transition-shadow duration-150 flex flex-col gap-3"
    >
      <div className="flex items-start justify-between gap-2">
        <div className="flex items-start gap-2 min-w-0">
          <Layers className="w-4 h-4 text-warm-text-muted shrink-0 mt-0.5" aria-hidden />
          <div className="min-w-0">
            <h3 className="text-sm font-semibold text-warm-text truncate">
              {scene.display_name}
            </h3>
            <p className="text-xs text-warm-text-muted mt-0.5 truncate font-mono">
              {scene.slug}
            </p>
          </div>
        </div>
        <Badge variant={sourceVariant} data-testid="scene-source-badge">
          {t(`source.${scene.source}`)}
        </Badge>
      </div>

      {scene.description ? (
        <p className="text-xs text-warm-text-muted line-clamp-2 min-h-[2rem]">
          {scene.description}
        </p>
      ) : (
        <div className="min-h-[2rem]" />
      )}

      {tags.length > 0 && (
        <div className="flex flex-wrap gap-1">
          {visibleTags.map((tag) => (
            <Badge key={tag} variant="outline" className="text-[11px]">
              {tag}
            </Badge>
          ))}
          {hiddenTagsCount > 0 && (
            <Badge variant="outline" className="text-[11px]">
              {t('card.tag_more', { count: hiddenTagsCount })}
            </Badge>
          )}
        </div>
      )}

      <div className="flex items-center gap-2 mt-auto pt-1">
        {onAttach && (
          <Button
            size="sm"
            variant="default"
            onClick={() => onAttach(scene)}
            className="flex-1"
          >
            {t('card.attach')}
          </Button>
        )}
        <Button asChild size="sm" variant="ghost">
          <Link to="/scenes/$id" params={{ id: String(scene.id) }}>
            {t('card.view')}
          </Link>
        </Button>
      </div>
    </div>
  );
}
