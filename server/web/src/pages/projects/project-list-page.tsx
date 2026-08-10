import { useState } from 'react';
import { Link } from '@tanstack/react-router';
import { useTranslation } from 'react-i18next';
import { useProjects } from '@/lib/hooks/use-projects';
import { useAuthStore } from '@/stores/auth-store';
import { OwnerBadge } from '@/components/shared/owner-badge';
import { OwnerFilter } from '@/components/shared/owner-filter';
import type { OwnerRef } from '@/types/org';

export function ProjectListPage() {
  const { t } = useTranslation('projects');
  const { projects, isLoading } = useProjects();
  const currentUser = useAuthStore((s) => s.user);
  const userId = currentUser?.id ?? 0;
  const [ownerFilter, setOwnerFilter] = useState<OwnerRef[] | 'all'>('all');

  const visibleProjects = (projects ?? []).filter((project) => {
    if (ownerFilter === 'all') return true;
    if (!project.owner) return false;
    return ownerFilter.some(
      (f) => f.type === project.owner!.type && f.id === project.owner!.id
    );
  });

  return (
    <div className="p-6">
      <div className="flex items-center justify-between mb-4">
        <h2 className="text-xl font-semibold text-foreground">{t('title')}</h2>
        <OwnerFilter value={ownerFilter} onChange={setOwnerFilter} userId={userId} />
      </div>

      {isLoading ? (
        <div className="text-sm text-muted-foreground">{t('common:actions.loading')}</div>
      ) : !projects || projects.length === 0 ? (
        <div className="text-sm text-muted-foreground">{t('list.empty')}</div>
      ) : (
        <div className="grid grid-cols-1 sm:grid-cols-2 md:grid-cols-3 gap-4">
          {visibleProjects.map((project) => (
            <Link
              key={project.id}
              // eslint-disable-next-line @typescript-eslint/no-explicit-any
              {...({ to: '/projects/$id', params: { id: project.id } } as any)}
              className="block p-4 border rounded-lg bg-card hover:bg-accent hover:border-info/40 transition-colors shadow-sm"
            >
              <div className="flex items-center gap-2 min-w-0">
                <h3 className="text-sm font-medium text-foreground truncate flex-1">{project.name}</h3>
                {project.owner && <OwnerBadge owner={project.owner} />}
              </div>
              {project.description && (
                <p className="text-xs text-muted-foreground mt-1 line-clamp-2">{project.description}</p>
              )}
            </Link>
          ))}
        </div>
      )}
    </div>
  );
}
