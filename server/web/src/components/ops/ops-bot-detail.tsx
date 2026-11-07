import type { ComponentType, ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { ArrowLeft, CalendarDays, MessageSquare, ShieldCheck, TrendingUp } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { OpsBudgetBar } from './budget-bar'
import { OpsRiskBadge } from './risk-badge'
import { OpsStatusBadge } from './status-badge'
import { opsApi } from '@/lib/api'
import type { OpsApproval, OpsBudgetSnapshot } from '@/types/ops'

const formatTime = (value?: string) => (value ? new Date(value).toLocaleString() : '—')

const formatValue = (value: unknown): string => {
  if (value === null || value === undefined) return '—'
  if (typeof value === 'number' || typeof value === 'string' || typeof value === 'boolean') {
    return String(value)
  }
  return JSON.stringify(value)
}

function Section({
  title,
  icon: Icon,
  children,
}: {
  title: string
  icon: ComponentType<{ className?: string }>
  children: ReactNode
}) {
  return (
    <section className="rounded-lg border bg-card p-4 space-y-3" aria-label={title}>
      <div className="flex items-center gap-2">
        <Icon className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
        <h2 className="text-sm font-medium">{title}</h2>
      </div>
      {children}
    </section>
  )
}

function EmptyLine({ children }: { children: ReactNode }) {
  return <p className="text-xs text-muted-foreground">{children}</p>
}

function SummaryCard({
  label,
  value,
  children,
}: {
  label: string
  value?: string
  children?: ReactNode
}) {
  return (
    <div className="rounded-lg border bg-warm-surface p-3 space-y-1">
      <div className="text-xs text-muted-foreground">{label}</div>
      {value ? <div className="text-lg font-semibold tabular-nums">{value}</div> : null}
      {children}
    </div>
  )
}

function ApprovalRow({
  approval,
  busy,
  onDecide,
}: {
  approval: OpsApproval
  busy: boolean
  onDecide: (approval: OpsApproval, action: 'approve' | 'reject') => void
}) {
  const { t } = useTranslation('ops')
  return (
    <li className="flex flex-wrap items-center gap-2 px-3 py-2">
      <div className="min-w-0 flex-1">
        <div className="text-sm font-medium">
          {t('detail.approvalType', { type: approval.type || `#${approval.id}` })}
        </div>
        <div className="text-xs text-muted-foreground">
          {t('detail.approvalRequestedAt', { time: formatTime(approval.requested_at) })}
        </div>
      </div>
      <Button size="sm" variant="ghost" disabled={busy} onClick={() => onDecide(approval, 'reject')}>
        {t('actions.reject')}
      </Button>
      <Button size="sm" variant="outline" disabled={busy} onClick={() => onDecide(approval, 'approve')}>
        {t('actions.approve')}
      </Button>
    </li>
  )
}

export function OpsBotDetailPage({ botId }: { botId: number }) {
  const { t } = useTranslation('ops')
  const queryClient = useQueryClient()

  const botQuery = useQuery({
    queryKey: ['ops-bot', botId],
    queryFn: () => opsApi.getBot(botId),
    enabled: Number.isFinite(botId) && botId > 0,
    retry: 1,
  })
  const bot = botQuery.data
  const ownerParam = bot ? `${bot.owner_type}:${bot.owner_id}` : null

  const listQuery = useQuery({
    queryKey: ['ops-bots', ownerParam],
    queryFn: () => opsApi.list(ownerParam),
    enabled: !!bot,
    staleTime: 30_000,
  })
  const account = listQuery.data?.owners
    .flatMap((owner) => owner.channels)
    .flatMap((channel) => channel.accounts)
    .find((row) => row.bot_id === botId)

  const budgetQuery = useQuery({
    queryKey: ['ops-bot-budget', botId],
    queryFn: () => opsApi.budget(botId),
    enabled: !!bot,
    retry: 1,
  })
  const metricsQuery = useQuery({
    queryKey: ['ops-bot-metrics', botId],
    queryFn: () => opsApi.metrics(botId),
    enabled: !!bot,
    retry: 1,
  })
  const calendarQuery = useQuery({
    queryKey: ['ops-bot-calendar', botId],
    queryFn: () => opsApi.calendar(botId),
    enabled: !!bot,
    retry: 1,
  })
  const engagementsQuery = useQuery({
    queryKey: ['ops-bot-engagements', botId],
    queryFn: () => opsApi.engagements(botId),
    enabled: !!bot,
    retry: 1,
  })
  const approvalsQuery = useQuery({
    queryKey: ['ops-approvals', ownerParam],
    queryFn: () => opsApi.listApprovals(ownerParam),
    enabled: !!ownerParam,
    retry: 1,
  })
  const approvals = (approvalsQuery.data?.items ?? []).filter((item) => item.bot_id === botId)

  const toggle = useMutation({
    mutationFn: () => (bot?.status === 'active' ? opsApi.pause(botId) : opsApi.resume(botId)),
    onSuccess: () => {
      toast.success(
        bot?.status === 'active'
          ? t('toast.paused', { name: bot?.display_name || '' })
          : t('toast.resumed', { name: bot?.display_name || '' }),
      )
      queryClient.invalidateQueries({ queryKey: ['ops-bot', botId] })
      queryClient.invalidateQueries({ queryKey: ['ops-bots'] })
    },
    onError: (err: unknown) => {
      toast.error(
        t('toast.actionFailed', { message: err instanceof Error ? err.message : String(err) }),
      )
    },
  })

  const decide = useMutation({
    mutationFn: ({ approval, action }: { approval: OpsApproval; action: 'approve' | 'reject' }) =>
      action === 'approve' ? opsApi.approve(approval.id) : opsApi.reject(approval.id),
    onSuccess: (_result, vars) => {
      toast.success(vars.action === 'approve' ? t('toast.approved') : t('toast.rejected'))
      queryClient.invalidateQueries({ queryKey: ['ops-approvals'] })
      queryClient.invalidateQueries({ queryKey: ['ops-bot', botId] })
      queryClient.invalidateQueries({ queryKey: ['ops-bots'] })
    },
    onError: (err: unknown) => {
      toast.error(
        t('toast.actionFailed', { message: err instanceof Error ? err.message : String(err) }),
      )
    },
  })

  if (botQuery.isLoading) {
    return (
      <div className="h-full overflow-auto bg-warm-canvas" aria-busy="true">
        <div className="mx-auto max-w-5xl p-4 md:p-6 space-y-4">
          <div className="h-9 w-32 rounded-md bg-warm-muted" />
          <div className="h-28 rounded-lg border bg-card" />
          <div className="h-56 rounded-lg border bg-card" />
        </div>
      </div>
    )
  }

  if (botQuery.isError || !bot) {
    return (
      <div className="h-full overflow-auto bg-warm-canvas">
        <div className="mx-auto max-w-5xl p-4 md:p-6 space-y-4">
          <Button asChild variant="ghost" size="sm">
            <Link to="/ops">
              <ArrowLeft className="h-4 w-4" />
              {t('actions.back')}
            </Link>
          </Button>
          <div className="rounded-lg border bg-card p-4 flex items-center justify-between gap-3">
            <span className="text-sm text-muted-foreground">{t('detail.notFound')}</span>
            <Button variant="outline" size="sm" onClick={() => botQuery.refetch()}>
              {t('retry')}
            </Button>
          </div>
        </div>
      </div>
    )
  }

  const budgetSnapshot: OpsBudgetSnapshot | null = budgetQuery.data
    ? {
        limit: budgetQuery.data.limit,
        currency: budgetQuery.data.currency,
        spent: budgetQuery.data.spent,
        remaining: budgetQuery.data.remaining,
        unlimited: budgetQuery.data.unlimited,
        configured: budgetQuery.data.limit > 0,
        period_from: budgetQuery.data.period_from,
        period_to: budgetQuery.data.period_to,
      }
    : null

  const browser = account?.browser
  const risk = account?.risk ?? 'normal'
  const riskReasons = account?.risk_reasons ?? []
  const pendingCount = approvals.length || account?.pending_approvals || 0

  return (
    <div className="h-full overflow-auto bg-warm-canvas">
      <div className="mx-auto max-w-5xl p-4 md:p-6 space-y-4">
        <Button asChild variant="ghost" size="sm">
          <Link to="/ops">
            <ArrowLeft className="h-4 w-4" />
            {t('actions.back')}
          </Link>
        </Button>

        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0 space-y-1">
            <div className="flex flex-wrap items-center gap-2">
              <h1 className="truncate text-lg font-semibold">
                {bot.display_name || bot.account_ref || `#${bot.id}`}
              </h1>
              <OpsStatusBadge status={bot.status} />
              <OpsRiskBadge level={risk} reasons={riskReasons} />
            </div>
            <p className="text-sm text-muted-foreground">
              {[bot.account_ref, bot.pack_id || t('packUnbound')].filter(Boolean).join(' · ')}
            </p>
          </div>
          <Button
            variant="outline"
            size="sm"
            disabled={toggle.isPending}
            onClick={() => toggle.mutate()}
          >
            {bot.status === 'active' ? t('actions.pause') : t('actions.resume')}
          </Button>
        </div>

        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <SummaryCard
            label={t('columns.output')}
            value={t('output', {
              today: account?.output.today ?? 0,
              week: account?.output.week ?? 0,
            })}
          />
          <SummaryCard label={t('columns.budget')}>
            {budgetSnapshot ? (
              <OpsBudgetBar budget={budgetSnapshot} />
            ) : (
              <span className="text-xs text-muted-foreground">{t('budget.notConfigured')}</span>
            )}
          </SummaryCard>
          <SummaryCard label={t('columns.approvals')} value={String(pendingCount)} />
          <SummaryCard
            label={t('detail.browser')}
            value={
              browser
                ? `${t(`loginStatus.${browser.login_status}`, { defaultValue: browser.login_status })} · ${t(
                    `health.${browser.health}`,
                    { defaultValue: browser.health },
                  )}`
                : '—'
            }
          />
        </div>

        <Section title={t('detail.metrics')} icon={TrendingUp}>
          {metricsQuery.data?.items.length ? (
            <ul className="divide-y divide-border rounded-md border">
              {metricsQuery.data.items.map((item) => (
                <li key={`${item.date}-${item.source}`} className="px-3 py-2 space-y-1">
                  <div className="text-xs text-muted-foreground">
                    {t('detail.metricDate', { date: item.date, source: item.source || '—' })}
                  </div>
                  <div className="flex flex-wrap gap-x-3 gap-y-1 text-xs tabular-nums">
                    {Object.entries(item.metrics).map(([key, value]) => (
                      <span key={key} className="text-foreground">
                        <span className="text-muted-foreground">{key}: </span>
                        {formatValue(value)}
                      </span>
                    ))}
                  </div>
                </li>
              ))}
            </ul>
          ) : (
            <EmptyLine>{t('detail.metricsEmpty')}</EmptyLine>
          )}
        </Section>

        <Section title={t('detail.calendar')} icon={CalendarDays}>
          {calendarQuery.data?.items.length ? (
            <ul className="divide-y divide-border rounded-md border">
              {calendarQuery.data.items.map((item) => (
                <li
                  key={item.issue_id}
                  className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2 text-sm"
                >
                  <span className="min-w-0 flex-1 truncate">{item.title}</span>
                  <Badge variant="outline" className="rounded px-1.5 py-0.5 text-xs">
                    {t(`calendarStatus.${item.lifecycle_status}`, {
                      defaultValue: item.lifecycle_status,
                    })}
                  </Badge>
                  {item.due_date ? (
                    <span className="text-xs text-muted-foreground tabular-nums">
                      {t('detail.dueDate', { date: item.due_date })}
                    </span>
                  ) : null}
                </li>
              ))}
            </ul>
          ) : (
            <EmptyLine>{t('detail.calendarEmpty')}</EmptyLine>
          )}
        </Section>

        <Section title={t('detail.engagements')} icon={MessageSquare}>
          {engagementsQuery.data?.items.length ? (
            <ul className="divide-y divide-border rounded-md border">
              {engagementsQuery.data.items.map((item) => (
                <li key={item.id} className="space-y-1 px-3 py-2">
                  <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                    <span>
                      {t('detail.engagementAuthor', {
                        author: item.author || '—',
                        kind: item.kind,
                      })}
                    </span>
                    <Badge variant="outline" className="rounded px-1.5 py-0.5 text-xs">
                      {t(`engagementRisk.${item.risk_level}`, { defaultValue: item.risk_level })}
                    </Badge>
                    <span>
                      {t(`engagementStatus.${item.status}`, { defaultValue: item.status })}
                    </span>
                  </div>
                  <p className="text-sm">{item.text}</p>
                </li>
              ))}
            </ul>
          ) : (
            <EmptyLine>{t('detail.engagementsEmpty')}</EmptyLine>
          )}
        </Section>

        <Section title={t('detail.approvals')} icon={ShieldCheck}>
          {approvals.length ? (
            <ul className="divide-y divide-border rounded-md border">
              {approvals.map((approval) => (
                <ApprovalRow
                  key={approval.id}
                  approval={approval}
                  busy={decide.isPending}
                  onDecide={(row, action) => decide.mutate({ approval: row, action })}
                />
              ))}
            </ul>
          ) : (
            <EmptyLine>{t('detail.approvalsEmpty')}</EmptyLine>
          )}
        </Section>

        <Section title={t('detail.browser')} icon={ShieldCheck}>
          {browser ? (
            <ul className="space-y-1 text-xs">
              <li>{t('detail.browserBackend', { value: browser.backend })}</li>
              <li>
                {t('detail.browserLogin', {
                  value: t(`loginStatus.${browser.login_status}`, {
                    defaultValue: browser.login_status,
                  }),
                })}
              </li>
              <li>
                {t('detail.browserHealth', {
                  value: t(`health.${browser.health}`, { defaultValue: browser.health }),
                })}
              </li>
              <li>
                {t('detail.browserRisk', {
                  value: t(`riskState.${browser.risk_state}`, {
                    defaultValue: browser.risk_state,
                  }),
                })}
              </li>
              {browser.last_error ? (
                <li className="text-destructive-text">
                  {t('detail.browserError', { value: browser.last_error })}
                </li>
              ) : null}
            </ul>
          ) : (
            <EmptyLine>{t('detail.browserEmpty')}</EmptyLine>
          )}
        </Section>
      </div>
    </div>
  )
}
