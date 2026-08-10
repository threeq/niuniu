import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { api } from '@/lib/api'
import type { Project } from '@/types/api'
import { KanbanHeaderSlotContext } from '@/components/shared/kanban/kanban-header-slot'
import { ProjectKanbanPage } from './project-kanban-page'
import { ProjectMemoryTab } from '../project/memory-tab'
import { ProjectSettingsTab } from '../project/settings-tab'

interface ProjectDetailPageProps {
  projectId: string
}

export function ProjectDetailPage({ projectId }: ProjectDetailPageProps) {
  const { t } = useTranslation('projects')
  const [activeTab, setActiveTab] = useState<'kanban' | 'memory' | 'settings'>('kanban')
  // The kanban toolbar (rendered deep inside the board) portals itself into this
  // slot, merging into the tab row as a single 40px header. A ref-callback into
  // state so the portal target is available after the slot element mounts.
  const [headerSlot, setHeaderSlot] = useState<HTMLElement | null>(null)

  const { data: project, isLoading } = useQuery({
    queryKey: ['project', projectId],
    queryFn: () => api.get<Project>(`/projects/${projectId}`),
    retry: 1,
  })

  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-full text-muted-foreground text-sm">
        {t('common:actions.loading')}
      </div>
    )
  }

  if (!project) {
    return (
      <div className="flex items-center justify-center h-full text-muted-foreground text-sm">
        {t('detail.notFound')}
      </div>
    )
  }

  const projectIdNum = parseInt(projectId, 10)

  return (
    <div className="flex flex-col h-full">
      {/* Tab navigation — fixed 40px chrome row (matches the workspace header
          height). On the 看板 tab, the board toolbar portals into the right-hand
          slot so tabs + toolbar share this single row instead of stacking. */}
      <div className="h-10 shrink-0 border-b border-warm-border bg-warm-muted px-4 flex items-center gap-1">
        {(['kanban', 'memory', 'settings'] as const).map((tab) => (
          <button
            key={tab}
            onClick={() => setActiveTab(tab)}
            className={`h-full flex items-center px-3 text-sm font-medium border-b-2 -mb-px transition-colors shrink-0 ${
              activeTab === tab
                ? 'border-b-info text-info'
                : 'border-b-transparent text-muted-foreground hover:text-foreground'
            }`}
          >
            {t(`detail.tabs.${tab}`)}
          </button>
        ))}
        {/* Slot for the kanban toolbar (filled via portal from KanbanBoard). */}
        <div ref={setHeaderSlot} className="flex min-w-0 flex-1 items-center" />
      </div>

      {/* Tab content */}
      <KanbanHeaderSlotContext.Provider value={headerSlot}>
        <div className="flex-1 min-w-0 overflow-hidden">
          {activeTab === 'kanban' && <ProjectKanbanPage projectId={projectId} />}
          {activeTab === 'memory' && (
            <div className="h-full overflow-y-auto">
              <ProjectMemoryTab projectId={projectIdNum} owner={project.owner} />
            </div>
          )}
          {activeTab === 'settings' && (
            <div className="h-full overflow-y-auto">
              <ProjectSettingsTab projectId={projectIdNum} />
            </div>
          )}
        </div>
      </KanbanHeaderSlotContext.Provider>
    </div>
  )
}
