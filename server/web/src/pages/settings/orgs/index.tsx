import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from '@tanstack/react-router'
import { Plus, Building2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { useOrgStore } from '@/stores/org-store'
import { useAuthStore } from '@/stores/auth-store'
import { CreateOrgDialog } from './create-org-dialog'

const ROLE_VARIANT: Record<string, 'default' | 'secondary' | 'outline'> = {
  owner: 'default',
  admin: 'secondary',
  member: 'outline',
}

export function OrgsListPage() {
  const { t } = useTranslation('orgs')
  const navigate = useNavigate()
  const myOrgs = useOrgStore((s) => s.myOrgs)
  const allOrgs = useOrgStore((s) => s.allOrgs)
  const fetchAll = useOrgStore((s) => s.fetchAll)
  const authUser = useAuthStore((s) => s.user)
  const [createOpen, setCreateOpen] = useState(false)

  const isAdmin = authUser?.role === 'admin'

  // Global admins see every org (read-only visibility of ones they don't belong
  // to); everyone else sees only their memberships.
  useEffect(() => {
    if (isAdmin) fetchAll()
  }, [isAdmin, fetchAll])

  const orgs = isAdmin ? allOrgs : myOrgs

  const roleLabels: Record<string, string> = {
    owner: t('list.roleOwner'),
    admin: t('list.roleAdmin'),
    member: t('list.roleMember'),
  }

  return (
    <div className="flex flex-col h-full">
      <div className="shrink-0 px-8 pt-8">
        <div className="flex items-center justify-between mb-6">
          <h1 className="text-2xl font-semibold">{t('list.title')}</h1>
          {isAdmin && (
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              <Plus className="h-4 w-4 mr-1" />
              {t('list.createButton')}
            </Button>
          )}
        </div>
      </div>

      <div className="flex-1 overflow-y-auto px-8 pb-8">
        <div className="max-w-3xl">
          {orgs.length === 0 ? (
            <div className="border border-dashed rounded-lg p-12 text-center">
              <Building2 className="h-8 w-8 text-muted-foreground mx-auto mb-4" />
              {isAdmin ? (
                <>
                  <p className="text-sm font-medium text-foreground mb-1">{t('list.emptyAdminTitle')}</p>
                  <p className="text-sm text-muted-foreground mb-4">{t('list.emptyAdminHint')}</p>
                  <Button onClick={() => setCreateOpen(true)}>
                    <Plus className="h-4 w-4 mr-1" />
                    {t('list.createFirstButton')}
                  </Button>
                </>
              ) : (
                <>
                  <p className="text-sm font-medium text-foreground mb-1">{t('list.emptyMemberTitle')}</p>
                  <p className="text-sm text-muted-foreground">{t('list.emptyMemberHint')}</p>
                </>
              )}
            </div>
          ) : (
            <div className="space-y-3">
              {orgs.map((org) => (
                <div
                  key={org.id}
                  onClick={() => navigate({ to: '/settings/orgs/$slug', params: { slug: org.slug } })}
                  className="flex items-center justify-between px-4 py-3 border border-border rounded-lg bg-card hover:bg-accent cursor-pointer transition-colors"
                >
                  <div className="flex items-center gap-3 min-w-0">
                    <Building2 className="h-5 w-5 text-muted-foreground shrink-0" />
                    <div className="min-w-0">
                      <p className="text-sm font-medium text-foreground truncate">{org.name}</p>
                      {org.description && (
                        <p className="text-xs text-muted-foreground truncate mt-0.5">
                          {org.description}
                        </p>
                      )}
                    </div>
                  </div>
                  <div className="flex items-center gap-2 shrink-0 ml-4">
                    {org.role && (
                      <Badge variant={ROLE_VARIANT[org.role] ?? 'outline'}>
                        {roleLabels[org.role] ?? org.role}
                      </Badge>
                    )}
                    <span className="text-xs text-muted-foreground font-mono">{org.slug}</span>
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>

      <CreateOrgDialog open={createOpen} onOpenChange={setCreateOpen} />
    </div>
  )
}
