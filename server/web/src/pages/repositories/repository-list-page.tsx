import { useEffect, useState } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { useTranslation } from 'react-i18next';
import { Plus } from 'lucide-react';
import { useRepositories } from '@/lib/hooks/use-repositories';
import { useAppStore } from '@/stores/app';
import { useAuthStore } from '@/stores/auth-store';
import { CreateRepositoryDialog } from '@/components/dialogs/create-repository-dialog';
import { OwnerFilter } from '@/components/shared/owner-filter';
import type { OwnerRef } from '@/types/org';

export function RepositoryListPage() {
  const { t } = useTranslation('repositories');
  const { repositories, isLoading } = useRepositories();
  const { lastOpenedRepository } = useAppStore();
  const navigate = useNavigate();
  const [createOpen, setCreateOpen] = useState(false);
  const currentUser = useAuthStore((s) => s.user);
  const userId = currentUser?.id ?? 0;
  const [ownerFilter, setOwnerFilter] = useState<OwnerRef[] | 'all'>('all');

  useEffect(() => {
    if (isLoading || !repositories) return;

    // Try last opened
    if (lastOpenedRepository && repositories.some((r) => String(r.id) === lastOpenedRepository)) {
      navigate({ to: '/repositories/$id', params: { id: lastOpenedRepository }, replace: true });
      return;
    }

    // Try first item
    if (repositories.length > 0) {
      navigate({ to: '/repositories/$id', params: { id: String(repositories[0].id) }, replace: true });
      return;
    }

    // Empty — stay on this page
  }, [isLoading, repositories, lastOpenedRepository, navigate]);

  if (isLoading) {
    return (
      <div className="flex h-full items-center justify-center text-muted-foreground text-sm">
        {t('common:actions.loading')}
      </div>
    );
  }

  // If repositories exist, useEffect will redirect
  if (repositories && repositories.length > 0) {
    return <div className="flex h-full" />;
  }

  // Empty state
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 text-muted-foreground">
      <OwnerFilter value={ownerFilter} onChange={setOwnerFilter} userId={userId} />
      <p className="text-sm">{t('list.empty')}</p>
      <button
        onClick={() => setCreateOpen(true)}
        className="inline-flex items-center gap-1.5 px-3 py-1.5 text-sm bg-info text-white rounded-md hover:bg-info/90 transition-colors"
      >
        <Plus className="w-4 h-4" />
        {t('list.addRepository')}
      </button>
      <CreateRepositoryDialog open={createOpen} onOpenChange={setCreateOpen} />
    </div>
  );
}
