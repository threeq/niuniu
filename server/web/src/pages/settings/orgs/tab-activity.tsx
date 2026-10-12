// TabActivity — Settings → Organizations → <org> → 集成活动 (issue #743).
//
// The cross-user integration audit view: every member's external API calls,
// data queries and approval decisions in one newest-first feed, answering
// "who called what, when, with which credential, and how did it end".
//
// Reads the SAME backend service as the personal view (#728); the team edition
// only adds the org dimension and the admin gate. Records are append-only, so
// this view offers no edit or delete affordance — export is the only write-ish
// action, and it leaves its own entry in the org audit log.

import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import {
  Activity,
  ChevronLeft,
  ChevronRight,
  Download,
  KeyRound,
  ShieldCheck,
} from 'lucide-react'
import { toast } from 'sonner'
import { api } from '@/lib/api'
import {
  exportFilename,
  orgIntegrationAuditApi,
} from '@/lib/org-integration-audit-api'
import { saveBlobToLocal } from '@/lib/download-file'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { useProjects } from '@/lib/hooks/use-projects'
import { EmptyState } from '@/components/shared/empty-state'
import { LoadingSkeleton } from '@/components/shared/loading-skeleton'
import type { Org, OrgMember } from '@/types/org'
import type {
  OrgActivityFilters,
  OrgActivityItem,
  OrgActivityKind,
  OrgActivityOutcome,
} from '@/types/org-integration-activity'

interface TabActivityProps {
  org: Org
  role: string | undefined
}

const PAGE_SIZE = 20
const ALL = 'all'

type Tone = 'success' | 'warning' | 'destructive' | 'muted'

/** Colour never carries the meaning alone — every tone also renders text
 *  (design-system §5.10 "色 + 文字双编码"). */
const TONE_CLASS: Record<Tone, string> = {
  success: 'bg-success/15 text-success border-success/30',
  warning: 'bg-warning/15 text-warning border-warning/30',
  destructive: 'bg-destructive/15 text-destructive border-destructive/30',
  muted: 'bg-warm-muted text-warm-text-muted border-warm-border',
}

function outcomeTone(outcome: OrgActivityOutcome): Tone {
  if (outcome === 'ok') return 'success'
  if (outcome === 'error') return 'destructive'
  return 'warning'
}

/** The credential (api) or data source (data) owner, as a display scope. */
function rowScope(item: OrgActivityItem): 'org' | 'personal' | null {
  const owner =
    item.kind === 'data' ? item.source_owner_type : item.credential_owner_type
  if (owner === 'org') return 'org'
  if (owner === 'user') return 'personal'
  return null
}

export function TabActivity({ org, role }: TabActivityProps) {
  const { t } = useTranslation('orgs')

  const [kind, setKind] = useState<OrgActivityKind | typeof ALL>(ALL)
  const [memberID, setMemberID] = useState<string>(ALL)
  const [projectID, setProjectID] = useState<string>(ALL)
  const [scope, setScope] = useState<string>(ALL)
  const [outcome, setOutcome] = useState<OrgActivityOutcome | typeof ALL>(ALL)
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [page, setPage] = useState(0)
  const [exporting, setExporting] = useState(false)

  const canView = role === 'owner' || role === 'admin'

  const { data: members = [] } = useQuery({
    queryKey: ['org-members', org.id],
    queryFn: () => api.listMembers(org.id),
    enabled: canView,
  })

  // Only this org's projects: the filter narrows the feed to integrations
  // bound to the chosen project, and another owner's project can never have
  // bindings here.
  const { projects: allProjects } = useProjects()
  const projects = useMemo(
    () => (allProjects ?? []).filter((p) => p.owner?.type === 'org' && p.owner.id === org.id),
    [allProjects, org.id],
  )

  const filters = useMemo<OrgActivityFilters>(() => {
    const f: OrgActivityFilters = {}
    if (kind !== ALL) f.kind = kind
    if (memberID !== ALL) f.member_id = Number(memberID)
    if (projectID !== ALL) f.project_id = Number(projectID)
    if (scope !== ALL) f.credential_scope = scope as 'org' | 'user'
    if (outcome !== ALL) f.outcome = outcome
    // Date-only bounds: the server widens a `to` date to the end of that day,
    // so a same-day from/to pair still includes everything that day.
    if (from) f.from = from
    if (to) f.to = to
    return f
  }, [kind, memberID, projectID, scope, outcome, from, to])

  const { data, isLoading, isError, error, isFetching } = useQuery({
    queryKey: ['org-integration-activity', org.id, filters, page],
    queryFn: () =>
      orgIntegrationAuditApi.list(
        org.id,
        filters,
        PAGE_SIZE,
        page * PAGE_SIZE,
      ),
    enabled: canView,
    placeholderData: (prev) => prev,
  })

  if (!canView) {
    return (
      <div className="py-6">
        <p className="text-sm text-muted-foreground">{t('activity.noPermission')}</p>
      </div>
    )
  }

  const items = data?.items ?? []
  const total = data?.total ?? 0
  const firstShown = total === 0 ? 0 : page * PAGE_SIZE + 1
  const lastShown = page * PAGE_SIZE + items.length
  const hasPrev = page > 0
  const hasNext = (page + 1) * PAGE_SIZE < total

  const onExport = async () => {
    setExporting(true)
    try {
      const blob = await orgIntegrationAuditApi.exportCsv(org.id, filters)
      saveBlobToLocal(blob, exportFilename(org.id))
      toast.success(t('activity.exportDone', { count: total }))
    } catch (e) {
      toast.error(t('activity.exportFailed', { error: (e as Error).message }))
    } finally {
      setExporting(false)
    }
  }

  const resetPage = () => setPage(0)

  const memberName = (id?: number): string => {
    if (!id) return ''
    const m: OrgMember | undefined = members.find((x) => x.user_id === id)
    return m ? m.display_name || m.username : t('activity.unknownUser', { id })
  }

  return (
    <div className="py-6 space-y-4">
      <div className="flex items-start justify-between gap-4">
        <p className="text-sm text-muted-foreground">{t('activity.hint')}</p>
        <Button
          size="sm"
          variant="outline"
          onClick={onExport}
          disabled={exporting || total === 0}
          className="shrink-0"
        >
          <Download className="h-4 w-4" />
          {exporting ? t('activity.exporting') : t('activity.export')}
        </Button>
      </div>

      {/* Filters. Each maps 1:1 onto a server query parameter, and the export
          reuses the same set, so what is on screen is what gets exported. */}
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3">
        <div className="space-y-1">
          <Label htmlFor="activity-kind">{t('activity.filters.kind')}</Label>
          <Select
            value={kind}
            onValueChange={(v) => {
              setKind(v as OrgActivityKind | typeof ALL)
              resetPage()
            }}
          >
            <SelectTrigger id="activity-kind">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t('activity.filters.kindAll')}</SelectItem>
              <SelectItem value="api">{t('activity.kinds.api')}</SelectItem>
              <SelectItem value="data">{t('activity.kinds.data')}</SelectItem>
              <SelectItem value="approval">{t('activity.kinds.approval')}</SelectItem>
            </SelectContent>
          </Select>
        </div>

        <div className="space-y-1">
          <Label htmlFor="activity-member">{t('activity.filters.member')}</Label>
          <Select
            value={memberID}
            onValueChange={(v) => {
              setMemberID(v)
              resetPage()
            }}
          >
            <SelectTrigger id="activity-member">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t('activity.filters.memberAll')}</SelectItem>
              {members.map((m) => (
                <SelectItem key={m.user_id} value={String(m.user_id)}>
                  {m.display_name || m.username}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        <div className="space-y-1">
          <Label htmlFor="activity-project">{t('activity.filters.project')}</Label>
          <Select
            value={projectID}
            onValueChange={(v) => {
              setProjectID(v)
              resetPage()
            }}
          >
            <SelectTrigger id="activity-project">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t('activity.filters.projectAll')}</SelectItem>
              {projects.map((p) => (
                <SelectItem key={p.id} value={String(p.id)}>
                  {p.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        <div className="space-y-1">
          <Label htmlFor="activity-scope">{t('activity.filters.credentialScope')}</Label>
          <Select
            value={scope}
            onValueChange={(v) => {
              setScope(v)
              resetPage()
            }}
          >
            <SelectTrigger id="activity-scope">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t('activity.filters.credentialScopeAll')}</SelectItem>
              <SelectItem value="org">{t('activity.scope.org')}</SelectItem>
              <SelectItem value="user">{t('activity.scope.personal')}</SelectItem>
            </SelectContent>
          </Select>
        </div>

        <div className="space-y-1">
          <Label htmlFor="activity-outcome">{t('activity.filters.outcome')}</Label>
          <Select
            value={outcome}
            onValueChange={(v) => {
              setOutcome(v as OrgActivityOutcome | typeof ALL)
              resetPage()
            }}
          >
            <SelectTrigger id="activity-outcome">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t('activity.filters.outcomeAll')}</SelectItem>
              <SelectItem value="ok">{t('activity.outcomes.ok')}</SelectItem>
              <SelectItem value="error">{t('activity.outcomes.error')}</SelectItem>
              <SelectItem value="pending">{t('activity.outcomes.pending')}</SelectItem>
            </SelectContent>
          </Select>
        </div>

        <div className="space-y-1">
          <Label htmlFor="activity-from">{t('activity.filters.from')}</Label>
          <Input
            id="activity-from"
            type="date"
            value={from}
            onChange={(e) => {
              setFrom(e.target.value)
              resetPage()
            }}
          />
        </div>

        <div className="space-y-1">
          <Label htmlFor="activity-to">{t('activity.filters.to')}</Label>
          <Input
            id="activity-to"
            type="date"
            value={to}
            onChange={(e) => {
              setTo(e.target.value)
              resetPage()
            }}
          />
        </div>
      </div>

      {(kind !== ALL ||
        memberID !== ALL ||
        projectID !== ALL ||
        scope !== ALL ||
        outcome !== ALL ||
        from ||
        to) && (
        <Button
          size="sm"
          variant="ghost"
          onClick={() => {
            setKind(ALL)
            setMemberID(ALL)
            setProjectID(ALL)
            setScope(ALL)
            setOutcome(ALL)
            setFrom('')
            setTo('')
            resetPage()
          }}
        >
          {t('activity.filters.clear')}
        </Button>
      )}

      {isError ? (
        <p className="text-sm text-destructive">
          {t('activity.loadFailed', { error: (error as Error).message })}
        </p>
      ) : isLoading ? (
        <LoadingSkeleton variant="card" count={4} />
      ) : items.length === 0 ? (
        <EmptyState
          icon={<Activity className="h-10 w-10 text-muted-foreground" />}
          title={t('activity.empty')}
          size="compact"
        />
      ) : (
        <div className="space-y-3">
          {items.map((item) => {
            const scopeLabel = rowScope(item)
            return (
              <Card key={`${item.kind}-${item.id}`}>
                <CardContent className="pt-4 space-y-2">
                  <div className="flex items-center justify-between gap-3">
                    <div className="flex items-center gap-2 min-w-0">
                      <Badge variant="secondary" className="shrink-0">
                        {t(`activity.kinds.${item.kind}`)}
                      </Badge>
                      <span className="text-sm font-medium truncate">
                        {item.kind === 'api'
                          ? item.provider_name || `#${item.provider_id}`
                          : item.kind === 'data'
                            ? item.source_name || `#${item.source_id}`
                            : item.subject_label || item.action || ''}
                      </span>
                    </div>
                    <Badge
                      variant="outline"
                      className={`shrink-0 ${TONE_CLASS[outcomeTone(item.outcome)]}`}
                    >
                      {t(`activity.outcomes.${item.outcome}`)}
                      {item.kind === 'approval' && item.status
                        ? ` · ${item.status}`
                        : ''}
                    </Badge>
                  </div>

                  <p className="text-xs text-muted-foreground">
                    {memberName(item.user_id) || item.actor_label || '—'}
                    {' · '}
                    {new Date(item.created_at).toLocaleString()}
                    {scopeLabel && (
                      <>
                        {' · '}
                        <span className="inline-flex items-center gap-1">
                          <KeyRound className="h-3 w-3" />
                          {item.credential_alias || t(`activity.scope.${scopeLabel}`)}
                        </span>
                      </>
                    )}
                  </p>

                  {item.kind === 'api' && (
                    <p className="text-sm font-mono break-all">
                      {item.method} {item.path}
                      {item.status_code ? ` → ${item.status_code}` : ''}
                    </p>
                  )}
                  {item.kind === 'data' && item.statement_summary && (
                    <p className="text-sm font-mono break-all">
                      {item.statement_summary}
                      {item.rows != null ? ` · ${t('activity.rows', { count: item.rows })}` : ''}
                      {item.duration_ms != null
                        ? ` · ${t('activity.duration', { ms: item.duration_ms })}`
                        : ''}
                    </p>
                  )}
                  {item.kind === 'approval' && (
                    <p className="text-sm">
                      {item.summary || item.note || item.action}
                      {item.requested_by ? (
                        <span className="text-muted-foreground">
                          {' · '}
                          {t('activity.requestedBy', { user: memberName(item.requested_by) })}
                        </span>
                      ) : null}
                    </p>
                  )}

                  {item.kind === 'api' && (
                    <p className="text-xs text-muted-foreground">
                      <ShieldCheck className="inline h-3 w-3 mr-1" />
                      {item.body_sha256
                        ? t('activity.fingerprintDetail', {
                            sha: item.body_sha256.slice(0, 16),
                            bytes: item.body_bytes ?? 0,
                            fields: (item.body_fields ?? []).join(', ') || '—',
                          })
                        : t('activity.fingerprintNone')}
                    </p>
                  )}

                  {item.kind === 'approval' && item.diff && (
                    <details className="text-xs">
                      <summary className="cursor-pointer text-muted-foreground hover:text-foreground">
                        {t('activity.fingerprint')}
                      </summary>
                      <pre className="mt-2 p-3 rounded bg-muted overflow-x-auto whitespace-pre-wrap break-all font-mono">
                        {item.diff}
                      </pre>
                    </details>
                  )}
                </CardContent>
              </Card>
            )
          })}
        </div>
      )}

      {total > PAGE_SIZE && (
        <div className="flex items-center justify-between pt-2">
          <span className="text-xs text-muted-foreground">
            {firstShown}–{lastShown} · {t('activity.page', { total })}
            {isFetching ? ' …' : ''}
          </span>
          <div className="flex items-center gap-2">
            <Button
              size="sm"
              variant="outline"
              onClick={() => setPage((p) => Math.max(0, p - 1))}
              disabled={!hasPrev}
            >
              <ChevronLeft className="h-4 w-4" />
              {t('activity.prev')}
            </Button>
            <Button
              size="sm"
              variant="outline"
              onClick={() => setPage((p) => p + 1)}
              disabled={!hasNext}
            >
              {t('activity.next')}
              <ChevronRight className="h-4 w-4" />
            </Button>
          </div>
        </div>
      )}

      <p className="text-xs text-muted-foreground">{t('activity.auditNote')}</p>
    </div>
  )
}
