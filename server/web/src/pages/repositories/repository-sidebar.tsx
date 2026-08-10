import { useState, useEffect } from 'react';
import { Link, useParams } from '@tanstack/react-router';
import { useTranslation } from 'react-i18next';
import { Plus, Search } from 'lucide-react';
import { cn } from '@/lib/utils';
import { useRepositories } from '@/lib/hooks/use-repositories';
import { useOwnerGrouping } from '@/lib/hooks/use-owner-grouping';
import { CreateRepositoryDialog } from '@/components/dialogs/create-repository-dialog';
import { OwnerGroupSection } from '@/components/shared/owner-group-section';
import { getOwnerStyles, ownerKeyOf } from '@/lib/owner-color';
import { useAppStore } from '@/stores/app';
import type { Repository } from '@/types/api';

export function RepositorySidebar() {
  const { t } = useTranslation('repositories');
  const { repositories, isLoading } = useRepositories();
  const [search, setSearch] = useState('');
  const [createOpen, setCreateOpen] = useState(false);

  const params = useParams({ strict: false });
  const activeId = (params as Record<string, string | undefined>).id;

  const { setLastOpenedRepository } = useAppStore();

  useEffect(() => {
    if (activeId) {
      setLastOpenedRepository(activeId);
    }
  }, [activeId, setLastOpenedRepository]);

  const filtered = (repositories ?? []).filter((repo) =>
    repo.name.toLowerCase().includes(search.toLowerCase())
  );

  const grouping = useOwnerGrouping(filtered);

  const renderRepoItem = (repo: Repository) => {
    const isActive = activeId === String(repo.id);
    const styles = getOwnerStyles(ownerKeyOf(repo.owner));
    return (
      <Link
        key={repo.id}
        to="/repositories/$id"
        params={{ id: String(repo.id) }}
        className={cn(
          'flex flex-col px-3 py-1.5 mx-1 rounded-r-md transition-colors border-l-2',
          isActive
            ? cn('text-foreground', styles.bgActive, styles.borderActive)
            : cn('text-foreground hover:bg-accent border-l-transparent', styles.bgInactive)
        )}
      >
        <span className="text-sm font-medium truncate">{repo.name}</span>
        <span className="text-xs text-muted-foreground">
          {repo.default_branch ?? t('list.noBranch')}
          {repo.total_branches > 0 && ` · ${repo.total_branches} ${t('list.branchesCountSuffix')}`}
        </span>
      </Link>
    );
  };

  return (
    <aside className="border-r bg-muted flex flex-col h-full">
      {/* Header */}
      <div className="flex items-center justify-between px-3 py-2 border-b">
        <span className="text-sm font-semibold text-foreground">{t('list.title')}</span>
        <button
          className="p-0.5 rounded hover:bg-accent text-muted-foreground hover:text-foreground transition-colors"
          aria-label={t('list.addRepository')}
          title={t('list.addRepository')}
          onClick={() => setCreateOpen(true)}
        >
          <Plus className="w-4 h-4" />
        </button>
      </div>

      {/* Search */}
      <div className="px-2 py-1.5 border-b">
        <div className="flex items-center gap-1.5 bg-background border rounded-md px-2 py-1">
          <Search className="w-3.5 h-3.5 text-muted-foreground shrink-0" />
          <input
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t('list.searchPlaceholder')}
            className="flex-1 text-xs outline-none bg-transparent text-foreground placeholder:text-muted-foreground"
          />
        </div>
      </div>

      {/* Repository List */}
      <div className="flex-1 overflow-y-auto py-1.5">
        {isLoading ? (
          <div className="px-3 py-4 text-xs text-muted-foreground text-center">{t('common:actions.loading')}</div>
        ) : filtered.length === 0 ? (
          <div className="px-3 py-4 text-xs text-muted-foreground text-center">
            {search ? t('common:status.noMatchingResults') : t('list.empty')}
          </div>
        ) : grouping.mode === 'flat' ? (
          filtered.map(renderRepoItem)
        ) : (
          grouping.groups.map((g) => (
            <OwnerGroupSection
              key={g.key}
              ownerKey={g.key}
              label={g.label}
              icon={g.icon}
              count={g.items.length}
              storageKey={`repositories:${g.key}`}
            >
              {g.items.map(renderRepoItem)}
            </OwnerGroupSection>
          ))
        )}
      </div>
      <CreateRepositoryDialog open={createOpen} onOpenChange={setCreateOpen} />
    </aside>
  );
}
