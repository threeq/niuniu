import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from '@tanstack/react-router'
import { ShieldCheck, ShieldOff } from 'lucide-react'
import { api } from '@/lib/api'
import { projectFloorApi } from '@/lib/project-floor-api'
import type { Project } from '@/types/api'

// Cross-project 底线 overview.
//
// The rule library below is global and disabled by default, so this page used to
// say nothing about whether any project is actually gated. The floor is the part
// users configure in practice, but it is edited per project (项目 → 设置 → 看板),
// which made it invisible from here. This section closes that gap: it reports the
// live state of every project's floor and links straight to the editor.

export function ProjectFloorOverview() {
  const { t } = useTranslation('settings')

  const projects = useQuery({
    queryKey: ['projects', 'active'],
    queryFn: () => api.get<Project[]>('/projects', { params: { status: 'active' } }),
  })

  const ids = (projects.data ?? []).map((p) => p.id)

  const floors = useQuery({
    queryKey: ['project-floors', ids],
    queryFn: () => projectFloorApi.list(ids),
    enabled: ids.length > 0,
  })

  const floorFor = (projectId: number) =>
    (floors.data ?? []).find((f) => f.project_id === projectId)

  const rows = (projects.data ?? []).map((p) => ({
    project: p,
    floor: floorFor(p.id),
  }))
  const activeCount = rows.filter((r) => (r.floor?.command ?? '').trim() !== '').length

  return (
    <div className="space-y-4">
      <div>
        <h2 className="text-base font-semibold text-foreground">
          {t('harness.floors.title')}
        </h2>
        <p className="text-sm text-muted-foreground mt-0.5">
          {t('harness.floors.subtitle')}
        </p>
      </div>

      {projects.isLoading ? (
        <p className="text-sm text-muted-foreground">{t('common:actions.loading')}</p>
      ) : rows.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t('harness.floors.noProjects')}</p>
      ) : (
        <>
          <p className="text-xs text-muted-foreground">
            {t('harness.floors.summary', { active: activeCount, total: rows.length })}
          </p>
          <div className="space-y-2">
            {rows.map(({ project, floor }) => {
              const command = (floor?.command ?? '').trim()
              const active = command !== ''
              return (
                <div
                  key={project.id}
                  className="border border-border rounded-lg p-3 flex items-center gap-3"
                >
                  {active ? (
                    <ShieldCheck className="h-4 w-4 text-success shrink-0" aria-hidden="true" />
                  ) : (
                    <ShieldOff className="h-4 w-4 text-muted-foreground shrink-0" aria-hidden="true" />
                  )}
                  <div className="flex-1 min-w-0">
                    <div className="text-sm font-medium text-foreground truncate">
                      {project.name}
                    </div>
                    {active ? (
                      <code className="block text-xs text-muted-foreground truncate mt-0.5">
                        {command}
                      </code>
                    ) : (
                      <div className="text-xs text-muted-foreground mt-0.5">
                        {t('harness.floors.notSet')}
                      </div>
                    )}
                  </div>
                  <Link
                    to="/projects/$id"
                    params={{ id: String(project.id) }}
                    search={{ tab: 'settings', section: 'board' }}
                    className="text-xs text-info hover:underline shrink-0"
                  >
                    {active ? t('harness.floors.edit') : t('harness.floors.configure')}
                  </Link>
                </div>
              )
            })}
          </div>
        </>
      )}
    </div>
  )
}
