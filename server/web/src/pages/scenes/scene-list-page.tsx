import { useMemo, useState } from 'react';
import { Link } from '@tanstack/react-router';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { Plus, Search, Database } from 'lucide-react';
import { sceneApi } from '@/lib/api';
import type { Scene, SceneSource } from '@/types/api';
import { Input } from '@/components/ui/input';
import { Button } from '@/components/ui/button';
import { SceneCard } from './components/scene-card';

type SourceFilter = 'all' | SceneSource;

export function SceneListPage() {
  const { t } = useTranslation('scenes');
  const { data: scenes, isLoading } = useQuery({
    queryKey: ['scenes'],
    queryFn: () => sceneApi.list(),
  });

  const [search, setSearch] = useState('');
  const [tagFilter, setTagFilter] = useState<string>('');
  const [sourceFilter, setSourceFilter] = useState<SourceFilter>('all');

  const allTags = useMemo(() => {
    const set = new Set<string>();
    (scenes ?? []).forEach((s) => (s.tags ?? []).forEach((t) => set.add(t)));
    return Array.from(set).sort();
  }, [scenes]);

  const filtered = useMemo(() => {
    const list = scenes ?? [];
    const q = search.trim().toLowerCase();
    return list.filter((s) => {
      if (sourceFilter !== 'all' && s.source !== sourceFilter) return false;
      if (tagFilter && !(s.tags ?? []).includes(tagFilter)) return false;
      if (
        q &&
        !s.display_name.toLowerCase().includes(q) &&
        !s.slug.toLowerCase().includes(q)
      ) {
        return false;
      }
      return true;
    });
  }, [scenes, search, tagFilter, sourceFilter]);

  return (
    <div className="flex flex-col h-full overflow-y-auto">
      <div className="p-6 max-w-6xl mx-auto w-full space-y-4">
        <header className="flex items-start justify-between gap-4">
          <div>
            <h1 className="text-2xl font-semibold text-warm-text">
              {t('list.title')}
            </h1>
            <p className="text-sm text-warm-text-muted mt-1">{t('list.subtitle')}</p>
          </div>
          <div className="flex items-center gap-2 shrink-0">
            <Button asChild variant="outline">
              <Link to="/knowledge-bases">
                <Database className="w-4 h-4 mr-1" aria-hidden />
                {t('list.manageKb')}
              </Link>
            </Button>
            <Button asChild>
              <Link to="/scenes/new">
                <Plus className="w-4 h-4 mr-1" aria-hidden />
                {t('list.new')}
              </Link>
            </Button>
          </div>
        </header>

        <div className="flex flex-wrap items-center gap-2">
          <div className="relative flex-1 min-w-[200px]">
            <Search
              className="absolute left-2.5 top-1/2 -translate-y-1/2 w-4 h-4 text-warm-text-muted"
              aria-hidden
            />
            <Input
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder={t('list.search_placeholder')}
              className="pl-8"
            />
          </div>
          <select
            value={sourceFilter}
            onChange={(e) => setSourceFilter(e.target.value as SourceFilter)}
            className="h-9 rounded-md border border-input bg-background px-3 py-1 text-sm"
            aria-label={t('list.filter_source')}
          >
            <option value="all">{t('list.filter_source_all')}</option>
            <option value="builtin">{t('list.filter_source_builtin')}</option>
            <option value="user">{t('list.filter_source_user')}</option>
            <option value="registry">{t('list.filter_source_registry')}</option>
          </select>
          <select
            value={tagFilter}
            onChange={(e) => setTagFilter(e.target.value)}
            className="h-9 rounded-md border border-input bg-background px-3 py-1 text-sm"
          >
            <option value="">{t('list.tag_all')}</option>
            {allTags.map((tag) => (
              <option key={tag} value={tag}>
                {tag}
              </option>
            ))}
          </select>
        </div>

        {isLoading ? (
          <div className="text-sm text-warm-text-muted py-8 text-center">
            {t('list.loading')}
          </div>
        ) : filtered.length === 0 ? (
          <div className="text-sm text-warm-text-muted py-8 text-center">
            {t('list.empty')}
          </div>
        ) : (
          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
            {filtered.map((s: Scene) => (
              <SceneCard key={s.id} scene={s} />
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
