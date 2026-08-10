import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { FolderKanban, Server, GitFork } from 'lucide-react'
import { api } from '@/lib/api'
import type { Org } from '@/types/org'

interface TabResourcesProps {
  org: Org
}

export function TabResources({ org }: TabResourcesProps) {
  const { t } = useTranslation('orgs')
  const ownerParam = `org:${org.slug}`

  const { data: projects = [] } = useQuery({
    queryKey: ['org-projects', org.id],
    queryFn: () =>
      api.get<{ id: number; name: string }[]>('/projects', {
        params: { owner: ownerParam },
        suppressError: true,
      }),
  })

  const { data: workspaces = [] } = useQuery({
    queryKey: ['org-workspaces', org.id],
    queryFn: () =>
      api.get<{ id: number; name: string }[]>('/workspaces', {
        params: { owner: ownerParam },
        suppressError: true,
      }),
  })

  const { data: repositories = [] } = useQuery({
    queryKey: ['org-repositories', org.id],
    queryFn: () =>
      api.get<{ id: number; name: string }[]>('/repositories', {
        params: { owner: ownerParam },
        suppressError: true,
      }),
  })

  const items = [
    {
      icon: FolderKanban,
      label: t('detail.resources.projects'),
      count: projects.length,
      to: '/projects' as const,
    },
    {
      icon: Server,
      label: t('detail.resources.workspaces'),
      count: workspaces.length,
      to: '/workspaces' as const,
    },
    {
      icon: GitFork,
      label: t('detail.resources.repositories'),
      count: repositories.length,
      to: '/repositories' as const,
    },
  ]

  return (
    <div className="py-6 space-y-4">
      <p className="text-sm text-muted-foreground">{t('detail.resources.description')}</p>
      <div className="grid grid-cols-3 gap-4 max-w-lg">
        {items.map(({ icon: Icon, label, count, to }) => (
          <Link
            key={label}
            to={to}
            className="flex flex-col items-center justify-center p-6 border border-border rounded-lg bg-card hover:bg-accent transition-colors text-center"
          >
            <Icon className="h-6 w-6 text-muted-foreground mb-2" />
            <span className="text-2xl font-bold text-foreground">{count}</span>
            <span className="text-xs text-muted-foreground mt-0.5">{label}</span>
          </Link>
        ))}
      </div>
    </div>
  )
}
