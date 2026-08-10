import { useNavigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { ArrowLeft } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useOrgStore } from '@/stores/org-store'
import { useAuthStore } from '@/stores/auth-store'
import { api } from '@/lib/api'
import { TabGeneral } from './tab-general'
import { TabMembers } from './tab-members'
import { TabResources } from './tab-resources'
import { TabAudit } from './tab-audit'
import { TabDanger } from './tab-danger'

type OrgTab = 'general' | 'members' | 'resources' | 'audit' | 'danger'

interface OrgDetailPageProps {
  slug: string
}

export function OrgDetailPage({ slug }: OrgDetailPageProps) {
  const { t } = useTranslation('orgs')
  const navigate = useNavigate()
  const myOrgs = useOrgStore((s) => s.myOrgs)
  const allOrgs = useOrgStore((s) => s.allOrgs)
  const fetchAll = useOrgStore((s) => s.fetchAll)
  const authUser = useAuthStore((s) => s.user)
  const [activeTab, setActiveTab] = useState<OrgTab>('general')

  const isAdmin = authUser?.role === 'admin'

  // Global admins may open orgs they don't belong to; make sure the all-orgs
  // list is loaded (e.g. on a deep-link straight to the detail page).
  useEffect(() => {
    if (isAdmin && allOrgs.length === 0) fetchAll()
  }, [isAdmin, allOrgs.length, fetchAll])

  // Find the org from the caller's memberships first, then fall back to the
  // admin-only all-orgs list.
  const orgFromStore =
    myOrgs.find((o) => o.slug === slug) ??
    (isAdmin ? allOrgs.find((o) => o.slug === slug) : undefined)

  const tabs: { id: OrgTab; label: string }[] = [
    { id: 'general', label: t('detail.tabs.general') },
    { id: 'members', label: t('detail.tabs.members') },
    { id: 'resources', label: t('detail.tabs.resources') },
    { id: 'audit', label: t('detail.tabs.audit') },
    { id: 'danger', label: t('detail.tabs.danger') },
  ]

  // Fetch members so TabDanger can use them without an extra fetch
  const { data: members = [] } = useQuery({
    queryKey: ['org-members', orgFromStore?.id],
    queryFn: () => api.listMembers(orgFromStore!.id),
    enabled: !!orgFromStore,
  })

  if (!orgFromStore) {
    return (
      <div className="flex flex-col h-full px-8 pt-8">
        <button
          onClick={() => navigate({ to: '/settings/orgs' })}
          className="flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground mb-6"
        >
          <ArrowLeft className="h-4 w-4" />
          {t('detail.back')}
        </button>
        <p className="text-sm text-muted-foreground">{t('detail.notFound', { slug })}</p>
      </div>
    )
  }

  const org = orgFromStore
  // Global admins manage every org with owner-level rights, even ones they are
  // not a member of (backend authorizes via the global 'admin' role). Treat
  // their effective role as 'owner' so all management UI is enabled.
  const role = isAdmin ? 'owner' : org.role

  return (
    <div className="flex flex-col h-full">
      <div className="shrink-0 px-8 pt-8">
        <button
          onClick={() => navigate({ to: '/settings/orgs' })}
          className="flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground mb-4"
        >
          <ArrowLeft className="h-4 w-4" />
          {t('detail.back')}
        </button>

        <h1 className="text-2xl font-semibold mb-1">{org.name}</h1>
        {org.description && (
          <p className="text-sm text-muted-foreground mb-4">{org.description}</p>
        )}

        {/* Tab navigation */}
        <div className="flex items-center gap-1 border-b mb-2">
          {tabs.map((tab) => (
            <button
              key={tab.id}
              onClick={() => setActiveTab(tab.id)}
              className={cn(
                'px-4 py-2 text-sm font-medium border-b-2 -mb-px transition-colors',
                activeTab === tab.id
                  ? 'border-info text-info'
                  : 'border-transparent text-muted-foreground hover:text-foreground',
                tab.id === 'danger' &&
                  activeTab !== 'danger' &&
                  'hover:text-destructive hover:border-destructive/50'
              )}
            >
              {tab.label}
            </button>
          ))}
        </div>
      </div>

      {/* Tab content — scrollable */}
      <div className="flex-1 overflow-y-auto px-8 pb-8">
        <div className="max-w-3xl">
          {activeTab === 'general' && <TabGeneral org={org} role={role} />}
          {activeTab === 'members' && <TabMembers org={org} role={role} />}
          {activeTab === 'resources' && <TabResources org={org} />}
          {activeTab === 'audit' && <TabAudit org={org} role={role} />}
          {activeTab === 'danger' && (
            <TabDanger org={org} role={role} members={members} />
          )}
        </div>
      </div>
    </div>
  )
}
