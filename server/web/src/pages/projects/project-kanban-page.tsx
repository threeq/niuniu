import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api } from '@/lib/api';
import type { Project } from '@/types/api';
import { KanbanBoard } from '@/components/shared/kanban/KanbanBoard';

interface ProjectKanbanPageProps {
  projectId: string;
}

export function ProjectKanbanPage({ projectId }: ProjectKanbanPageProps) {
  const { t } = useTranslation('projects');
  const { data: project, isLoading } = useQuery({
    queryKey: ['project', projectId],
    queryFn: () => api.get<Project>(`/projects/${projectId}`),
    retry: 1,
  });

  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-full text-muted-foreground text-sm">
        {t('common:actions.loading')}
      </div>
    );
  }

  if (!project) {
    return (
      <div className="flex items-center justify-center h-full text-muted-foreground text-sm">
        {t('detail.notFound')}
      </div>
    );
  }

  return <KanbanBoard project={project} />;
}
