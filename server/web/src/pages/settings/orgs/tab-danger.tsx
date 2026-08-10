import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from '@tanstack/react-router'
import { toast } from 'sonner'
import { useQuery } from '@tanstack/react-query'
import { AlertTriangle } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import { confirm } from '@/lib/confirm'
import { useOrgStore } from '@/stores/org-store'
import { useAuthStore } from '@/stores/auth-store'
import type { Org, OrgMember } from '@/types/org'

interface TabDangerProps {
  org: Org
  role: string | undefined
  members: OrgMember[]
}

export function TabDanger({ org, role, members }: TabDangerProps) {
  const { t } = useTranslation('orgs')
  const navigate = useNavigate()
  const currentUser = useAuthStore((s) => s.user)
  const invalidate = useOrgStore((s) => s.invalidate)
  const fetchOrgs = useOrgStore((s) => s.fetch)

  const [transferTarget, setTransferTarget] = useState<number>(0)
  const [transferring, setTransferring] = useState(false)
  const [deleting, setDeleting] = useState(false)

  const { data: projects = [] } = useQuery({
    queryKey: ['org-projects', org.id],
    queryFn: () =>
      api.get<{ id: number }[]>('/projects', {
        params: { owner: `org:${org.slug}` },
        suppressError: true,
      }),
  })

  const { data: workspaces = [] } = useQuery({
    queryKey: ['org-workspaces', org.id],
    queryFn: () =>
      api.get<{ id: number }[]>('/workspaces', {
        params: { owner: `org:${org.slug}` },
        suppressError: true,
      }),
  })

  const { data: repositories = [] } = useQuery({
    queryKey: ['org-repositories', org.id],
    queryFn: () =>
      api.get<{ id: number }[]>('/repositories', {
        params: { owner: `org:${org.slug}` },
        suppressError: true,
      }),
  })

  if (role !== 'owner') {
    return (
      <div className="py-6">
        <p className="text-sm text-muted-foreground">{t('detail.danger.noPermission')}</p>
      </div>
    )
  }

  const otherMembers = members.filter((m) => m.user_id !== currentUser?.id)
  const hasResources =
    projects.length > 0 || workspaces.length > 0 || repositories.length > 0

  function buildResourceSummary() {
    const parts: string[] = []
    if (projects.length > 0)
      parts.push(t('detail.danger.deleteResourceProjects', { count: projects.length }))
    if (workspaces.length > 0)
      parts.push(t('detail.danger.deleteResourceWorkspaces', { count: workspaces.length }))
    if (repositories.length > 0)
      parts.push(t('detail.danger.deleteResourceRepositories', { count: repositories.length }))
    return parts.join(t('detail.danger.deleteResourceSeparator'))
  }

  async function handleTransfer() {
    if (!transferTarget) return
    const target = otherMembers.find((m) => m.user_id === transferTarget)
    if (!target) return
    if (
      !(await confirm(
        t('detail.danger.transferConfirm', {
          org: org.name,
          name: target.display_name || target.username,
        }),
      ))
    )
      return

    setTransferring(true)
    try {
      await api.transferOwnership(org.id, transferTarget)
      invalidate()
      await fetchOrgs()
      toast.success(t('detail.danger.transferred'))
      navigate({ to: '/settings/orgs' })
    } catch (err) {
      toast.error(
        t('detail.danger.transferFailed', {
          error: err instanceof Error ? err.message : String(err),
        }),
      )
    } finally {
      setTransferring(false)
    }
  }

  async function handleDelete() {
    if (hasResources) return
    if (!(await confirm({ description: t('detail.danger.deleteConfirm', { org: org.name }), destructive: true }))) return

    setDeleting(true)
    try {
      await api.deleteOrg(org.id)
      invalidate()
      await fetchOrgs()
      toast.success(t('detail.danger.deleted'))
      navigate({ to: '/settings/orgs' })
    } catch (err) {
      toast.error(
        t('detail.danger.deleteFailed', {
          error: err instanceof Error ? err.message : String(err),
        }),
      )
    } finally {
      setDeleting(false)
    }
  }

  return (
    <div className="py-6 space-y-8">
      {/* Transfer Ownership */}
      <div className="border border-border rounded-lg p-5 space-y-3">
        <div className="flex items-start gap-3">
          <AlertTriangle className="h-5 w-5 text-warning mt-0.5 shrink-0" />
          <div>
            <p className="text-sm font-medium text-foreground">
              {t('detail.danger.transferTitle')}
            </p>
            <p className="text-xs text-muted-foreground mt-0.5">
              {t('detail.danger.transferHint')}
            </p>
          </div>
        </div>
        {otherMembers.length === 0 ? (
          <p className="text-xs text-muted-foreground">
            {t('detail.danger.transferEmpty')}
          </p>
        ) : (
          <div className="flex items-center gap-2">
            <select
              value={transferTarget}
              onChange={(e) => setTransferTarget(Number(e.target.value))}
              className="flex-1 h-9 px-3 border border-border bg-background rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-ring"
            >
              <option value={0}>{t('detail.danger.transferSelectPlaceholder')}</option>
              {otherMembers.map((m) => (
                <option key={m.user_id} value={m.user_id}>
                  {t('detail.danger.transferOptionLabel', {
                    name: m.display_name || m.username,
                    role: m.role,
                  })}
                </option>
              ))}
            </select>
            <Button
              variant="outline"
              size="sm"
              onClick={handleTransfer}
              disabled={!transferTarget || transferring}
            >
              {transferring
                ? t('detail.danger.transferring')
                : t('detail.danger.transferButton')}
            </Button>
          </div>
        )}
      </div>

      {/* Delete Organization */}
      <div className="border border-destructive/30 rounded-lg p-5 space-y-3">
        <div className="flex items-start gap-3">
          <AlertTriangle className="h-5 w-5 text-destructive mt-0.5 shrink-0" />
          <div>
            <p className="text-sm font-medium text-foreground">
              {t('detail.danger.deleteTitle')}
            </p>
            <p className="text-xs text-muted-foreground mt-0.5">
              {t('detail.danger.deleteHint')}
            </p>
            {hasResources && (
              <p className="text-xs text-destructive mt-1">
                {t('detail.danger.deleteHasResources', { summary: buildResourceSummary() })}
              </p>
            )}
          </div>
        </div>
        <Button
          variant="destructive"
          size="sm"
          onClick={handleDelete}
          disabled={hasResources || deleting}
          title={hasResources ? t('detail.danger.deleteDisabledTitle') : undefined}
        >
          {deleting ? t('detail.danger.deleting') : t('detail.danger.deleteButton')}
        </Button>
      </div>
    </div>
  )
}
