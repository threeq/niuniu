import { useState } from 'react'
import { toast } from 'sonner'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import {
  Building2,
  FolderKanban,
  LayoutGrid,
  GitBranch,
  Trash2,
  AlertTriangle,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from '@/components/ui/dialog'
import {
  AlertDialog,
  AlertDialogTrigger,
  AlertDialogContent,
  AlertDialogHeader,
  AlertDialogFooter,
  AlertDialogTitle,
  AlertDialogDescription,
  AlertDialogCancel,
  AlertDialogAction,
} from '@/components/ui/alert-dialog'
import { authUsersApi, ApiError } from '@/lib/api'
import type {
  AuthUser,
  UserResourceType,
  UserResourceProject,
  UserResourceWorkspace,
  UserResourceRepository,
} from '@/types/api'

// Pull the machine-readable guard reason out of a 409 from POST /purge.
// Backend standard envelope: { error: { code, message, details: { reason } } }.
// Also tolerate a top-level `reason` / bare-string `error` for robustness.
function purgeReason(err: unknown): string | null {
  if (err instanceof ApiError && err.status === 409) {
    const body = err.body as
      | {
          reason?: string
          error?: string | { message?: string; details?: { reason?: string } }
        }
      | null
    if (body?.error && typeof body.error === 'object' && body.error.details?.reason) {
      return body.error.details.reason
    }
    if (body?.reason) return body.reason
    if (typeof body?.error === 'string') return body.error
  }
  return null
}

export function UserDetailDialog({
  target,
  onClose,
  onPurged,
}: {
  target: AuthUser | null
  onClose: () => void
  onPurged: () => void
}) {
  const { t } = useTranslation('settings')
  const qc = useQueryClient()
  const userId = target?.id ?? 0

  const { data, isLoading, isError } = useQuery({
    queryKey: ['user-resources', userId],
    queryFn: () => authUsersApi.getResources(userId),
    enabled: !!target,
  })

  const refresh = () =>
    qc.invalidateQueries({ queryKey: ['user-resources', userId] })

  return (
    <Dialog open={!!target} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-2xl max-h-[85vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{target?.username}</DialogTitle>
          <DialogDescription>
            {t('users.resources.subtitle')}
          </DialogDescription>
        </DialogHeader>

        {target && (
          <div className="flex flex-wrap items-center gap-2 text-sm">
            {target.display_name && (
              <span className="text-warm-text">{target.display_name}</span>
            )}
            <Badge variant="secondary">
              {target.role === 'admin'
                ? t('common:role.admin')
                : t('common:role.member')}
            </Badge>
            <span className="text-xs text-warm-text-muted tabular-nums">
              {t('users.resources.createdAt', {
                date: new Date(target.created_at).toLocaleString(),
              })}
            </span>
          </div>
        )}

        {isLoading && (
          <div className="text-sm text-warm-text-muted">
            {t('users.resources.loading')}
          </div>
        )}
        {isError && (
          <div className="text-sm text-destructive">
            {t('users.resources.loadFailed')}
          </div>
        )}

        {data && (
          <div className="space-y-6">
            {/* Organizations */}
            <Section
              icon={<Building2 className="h-4 w-4" aria-hidden="true" />}
              title={t('users.resources.sections.orgs')}
              count={data.orgs.length}
              emptyLabel={t('users.resources.empty.orgs')}
            >
              {data.orgs.map((org) => (
                <div
                  key={org.id}
                  className="flex items-center justify-between gap-2 px-3 py-2"
                >
                  <div className="min-w-0">
                    <div className="truncate text-sm text-warm-text">
                      {org.name}
                    </div>
                    <div className="truncate font-mono text-xs text-warm-text-muted">
                      {org.slug}
                    </div>
                  </div>
                  <div className="flex shrink-0 items-center gap-1.5">
                    {org.role === 'owner' && (
                      <Badge variant="secondary">
                        {t('users.resources.owner')}
                      </Badge>
                    )}
                    {org.is_last_owner && (
                      <Badge variant="destructive">
                        {t('users.resources.lastOwner')}
                      </Badge>
                    )}
                  </div>
                </div>
              ))}
            </Section>

            {/* Projects */}
            <Section
              icon={<FolderKanban className="h-4 w-4" aria-hidden="true" />}
              title={t('users.resources.sections.projects')}
              count={data.projects.length}
              emptyLabel={t('users.resources.empty.projects')}
            >
              {data.projects.map((p: UserResourceProject) => (
                <ResourceRow
                  key={p.id}
                  userId={userId}
                  type="project"
                  resourceId={p.id}
                  name={p.name}
                  meta={new Date(p.created_at).toLocaleDateString()}
                  onDeleted={refresh}
                />
              ))}
            </Section>

            {/* Workspaces */}
            <Section
              icon={<LayoutGrid className="h-4 w-4" aria-hidden="true" />}
              title={t('users.resources.sections.workspaces')}
              count={data.workspaces.length}
              emptyLabel={t('users.resources.empty.workspaces')}
            >
              {data.workspaces.map((w: UserResourceWorkspace) => (
                <ResourceRow
                  key={w.id}
                  userId={userId}
                  type="workspace"
                  resourceId={w.id}
                  name={w.name}
                  badge={
                    <Badge variant="secondary">
                      {t(`users.resources.status.${w.status}`, w.status)}
                    </Badge>
                  }
                  onDeleted={refresh}
                />
              ))}
            </Section>

            {/* Repositories */}
            <Section
              icon={<GitBranch className="h-4 w-4" aria-hidden="true" />}
              title={t('users.resources.sections.repositories')}
              count={data.repositories.length}
              emptyLabel={t('users.resources.empty.repositories')}
            >
              {data.repositories.map((r: UserResourceRepository) => (
                <ResourceRow
                  key={r.id}
                  userId={userId}
                  type="repository"
                  resourceId={r.id}
                  name={r.name}
                  meta={r.path}
                  onDeleted={refresh}
                />
              ))}
            </Section>

            {/* Lightweight resource counts */}
            <div>
              <h3 className="mb-2 text-sm font-medium text-warm-text">
                {t('users.resources.sections.otherResources')}
              </h3>
              <div className="flex flex-wrap gap-2">
                <CountChip
                  label={t('users.resources.counts.envPresets')}
                  value={data.counts.env_presets}
                />
                <CountChip
                  label={t('users.resources.counts.quickActions')}
                  value={data.counts.quick_actions}
                />
                <CountChip
                  label={t('users.resources.counts.agents')}
                  value={data.counts.agents}
                />
                <CountChip
                  label={t('users.resources.counts.scenes')}
                  value={data.counts.scenes}
                />
                <CountChip
                  label={t('users.resources.counts.dataSources')}
                  value={data.counts.data_sources}
                />
                <CountChip
                  label={t('users.resources.counts.savedQueries')}
                  value={data.counts.saved_queries}
                />
                <CountChip
                  label={t('users.resources.counts.dashboards')}
                  value={data.counts.dashboards}
                />
                <CountChip
                  label={t('users.resources.counts.knowledgeBases')}
                  value={data.counts.knowledge_bases}
                />
              </div>
            </div>

            {/* Danger zone */}
            {target && (
              <PurgeSection
                user={target}
                orgsCount={data.orgs.length}
                projectsCount={data.projects.length}
                workspacesCount={data.workspaces.length}
                repositoriesCount={data.repositories.length}
                onPurged={() => {
                  onPurged()
                  onClose()
                }}
              />
            )}
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}

function Section({
  icon,
  title,
  count,
  emptyLabel,
  children,
}: {
  icon: React.ReactNode
  title: string
  count: number
  emptyLabel: string
  children: React.ReactNode
}) {
  return (
    <div>
      <h3 className="mb-2 flex items-center gap-1.5 text-sm font-medium text-warm-text">
        <span className="text-warm-text-muted">{icon}</span>
        {title}
        <span className="tabular-nums text-warm-text-muted">({count})</span>
      </h3>
      {count === 0 ? (
        <p className="rounded-md border border-warm-border bg-warm-muted px-3 py-2 text-xs text-warm-text-muted">
          {emptyLabel}
        </p>
      ) : (
        <div className="divide-y divide-warm-border overflow-hidden rounded-md border border-warm-border">
          {children}
        </div>
      )}
    </div>
  )
}

function ResourceRow({
  userId,
  type,
  resourceId,
  name,
  meta,
  badge,
  onDeleted,
}: {
  userId: number
  type: UserResourceType
  resourceId: number
  name: string
  meta?: string
  badge?: React.ReactNode
  onDeleted: () => void
}) {
  const { t } = useTranslation('settings')
  const m = useMutation({
    mutationFn: () => authUsersApi.deleteResource(userId, type, resourceId),
    onSuccess: () => {
      toast.success(t('users.resources.deleteResource.success'))
      onDeleted()
    },
    onError: (err: Error) => toast.error(err.message),
  })

  return (
    <div className="flex items-center justify-between gap-2 px-3 py-2">
      <div className="min-w-0">
        <div className="flex items-center gap-1.5">
          <span className="truncate text-sm text-warm-text">{name}</span>
          {badge}
        </div>
        {meta && (
          <div className="truncate font-mono text-xs text-warm-text-muted">
            {meta}
          </div>
        )}
      </div>
      <AlertDialog>
        <AlertDialogTrigger asChild>
          <Button
            variant="ghost"
            size="icon"
            className="shrink-0 text-warm-text-muted hover:text-destructive"
            aria-label={t('users.resources.deleteResource.trigger')}
            disabled={m.isPending}
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </AlertDialogTrigger>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t('users.resources.deleteResource.title', {
                type: t(`users.resources.deleteResource.types.${type}`),
              })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t('users.resources.deleteResource.description', { name })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('common:actions.cancel')}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              onClick={() => m.mutate()}
            >
              {t('users.resources.deleteResource.confirm')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function CountChip({ label, value }: { label: string; value: number }) {
  return (
    <span className="inline-flex items-center gap-1.5 rounded-md border border-warm-border bg-warm-muted px-2 py-1 text-xs text-warm-text-muted">
      {label}
      <span className="tabular-nums text-warm-text">{value}</span>
    </span>
  )
}

function PurgeSection({
  user,
  orgsCount,
  projectsCount,
  workspacesCount,
  repositoriesCount,
  onPurged,
}: {
  user: AuthUser
  orgsCount: number
  projectsCount: number
  workspacesCount: number
  repositoriesCount: number
  onPurged: () => void
}) {
  const { t } = useTranslation('settings')
  const [open, setOpen] = useState(false)
  const [confirmText, setConfirmText] = useState('')

  const m = useMutation({
    mutationFn: () => authUsersApi.purge(user.id),
    onSuccess: () => {
      toast.success(t('users.resources.purge.success'))
      setOpen(false)
      setConfirmText('')
      onPurged()
    },
    onError: (err: Error) => {
      const reason = purgeReason(err)
      if (reason === 'self') {
        toast.error(t('users.resources.purge.reason.self'))
      } else if (reason === 'last_admin') {
        toast.error(t('users.resources.purge.reason.lastAdmin'))
      } else if (reason?.startsWith('last_owner_of_org:')) {
        toast.error(
          t('users.resources.purge.reason.lastOwnerOfOrg', {
            slug: reason.slice('last_owner_of_org:'.length),
          }),
        )
      } else {
        toast.error(err.message || t('users.resources.purge.reason.generic'))
      }
    },
  })

  const confirmed = confirmText === user.username

  return (
    <div className="rounded-lg border border-destructive/40 bg-destructive/5 p-3">
      <div className="flex items-start gap-2">
        <AlertTriangle
          className="mt-0.5 h-4 w-4 shrink-0 text-destructive"
          aria-hidden="true"
        />
        <div className="flex-1 space-y-2">
          <h3 className="text-sm font-medium text-warm-text">
            {t('users.resources.purge.dangerZone')}
          </h3>
          <p className="text-xs text-warm-text-muted">
            {t('users.resources.purge.dangerZoneHint')}
          </p>
          <Dialog
            open={open}
            onOpenChange={(o) => {
              setOpen(o)
              if (!o) setConfirmText('')
            }}
          >
            <Button variant="destructive" size="sm" onClick={() => setOpen(true)}>
              <Trash2 className="mr-1 h-4 w-4" />
              {t('users.resources.purge.trigger')}
            </Button>
            <DialogContent>
              <DialogHeader>
                <DialogTitle>{t('users.resources.purge.title')}</DialogTitle>
                <DialogDescription>
                  {t('users.resources.purge.description', {
                    name: user.display_name || user.username,
                  })}
                </DialogDescription>
              </DialogHeader>

              <div className="flex flex-wrap gap-2">
                <CountChip
                  label={t('users.resources.sections.orgs')}
                  value={orgsCount}
                />
                <CountChip
                  label={t('users.resources.sections.projects')}
                  value={projectsCount}
                />
                <CountChip
                  label={t('users.resources.sections.workspaces')}
                  value={workspacesCount}
                />
                <CountChip
                  label={t('users.resources.sections.repositories')}
                  value={repositoriesCount}
                />
              </div>

              <div className="space-y-1.5">
                <label
                  htmlFor="purge-confirm"
                  className="text-sm text-warm-text-muted"
                >
                  {t('users.resources.purge.confirmLabel', {
                    username: user.username,
                  })}
                </label>
                <Input
                  id="purge-confirm"
                  value={confirmText}
                  onChange={(e) => setConfirmText(e.target.value)}
                  placeholder={t('users.resources.purge.confirmPlaceholder')}
                  autoComplete="off"
                />
              </div>

              <DialogFooterRow>
                <Button variant="outline" onClick={() => setOpen(false)}>
                  {t('common:actions.cancel')}
                </Button>
                <Button
                  variant="destructive"
                  disabled={!confirmed || m.isPending}
                  onClick={() => m.mutate()}
                >
                  {t('users.resources.purge.confirm')}
                </Button>
              </DialogFooterRow>
            </DialogContent>
          </Dialog>
        </div>
      </div>
    </div>
  )
}

// Local footer row: the danger-zone confirm dialog nests inside the manage
// dialog, so we lay out its actions inline rather than importing DialogFooter
// (which would otherwise double-apply responsive spacing here).
function DialogFooterRow({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
      {children}
    </div>
  )
}
