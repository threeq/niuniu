import type { ComponentType } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, Bot, Github, Inbox, Mail, Webhook, Workflow } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { EmptyState } from '@/components/shared/empty-state'
import { LoadingSkeleton } from '@/components/shared/loading-skeleton'
import { intakeApi, type IntakeMode, type IntakeSourceKind, type IntakeSourceSettings } from '@/lib/intake-api'
import { cn } from '@/lib/utils'

const MODES: IntakeMode[] = ['off', 'observe', 'auto']

const SOURCE_ICONS: Record<IntakeSourceKind, ComponentType<{ className?: string }>> = {
  im_bot: Bot,
  github: Github,
  ci: Workflow,
  email: Mail,
  webhook: Webhook,
}

function SourceCard({ source }: { source: IntakeSourceSettings }) {
  const { t } = useTranslation('intake')
  const Icon = SOURCE_ICONS[source.source_kind]
  const mode = source.mode
  const descriptionKey = `intake.mode.${mode}Description` as const

  return (
    <div className="border rounded-lg p-4 space-y-3 bg-card">
      <div className="flex items-center gap-2">
        <Icon className="w-4 h-4 text-muted-foreground" aria-hidden="true" />
        <span className="text-sm font-medium">{t(`intake.source.${source.source_kind}`)}</span>
        <span
          className={cn(
            'ml-auto inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-xs border',
            source.connected
              ? 'bg-success/15 text-success border-success/30'
              : 'bg-warm-muted text-warm-text-muted border-warm-border',
          )}
        >
          {source.connected ? t('intake.connected') : t('intake.notConnected')}
        </span>
      </div>

      {source.connected ? (
        <>
          <div
            className="inline-flex overflow-hidden rounded-md border border-warm-border"
            role="group"
            aria-label={t('intake.mode.label')}
          >
            {MODES.map((m) => (
              <span
                key={m}
                aria-current={mode === m ? 'true' : undefined}
                className={cn(
                  'px-2 py-1 text-xs font-medium border-r border-warm-border last:border-r-0',
                  mode === m
                    ? 'bg-brand-soft text-brand'
                    : 'bg-warm-muted text-warm-text-muted',
                )}
              >
                {t(`intake.mode.${m}`)}
              </span>
            ))}
          </div>
          <p className="text-xs text-muted-foreground">{t(descriptionKey)}</p>
          <p className="text-xs text-muted-foreground tabular-nums">
            {t('intake.summary', {
              cap: source.daily_cap,
              rules: source.rule_count,
              members: source.allowed_member_count,
            })}
          </p>
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="outline" size="sm" disabled>
              {t('intake.configure')}
            </Button>
            <Button variant="ghost" size="sm" disabled>
              {t('intake.pending', { count: source.pending_count })}
            </Button>
          </div>
        </>
      ) : (
        <div className="flex justify-end">
          <Button variant="outline" size="sm" disabled>
            {t('intake.connect')}
          </Button>
        </div>
      )}
    </div>
  )
}

interface Props {
  projectId: number
}

export function IntakePolicyPanel({ projectId }: Props) {
  const { t } = useTranslation('intake')
  const { data, isLoading, isError, refetch } = useQuery({
    queryKey: ['intake-settings', projectId],
    queryFn: () => intakeApi.getSettings(projectId),
    retry: false,
  })

  const sources = Array.isArray(data?.sources) ? data.sources : []

  return (
    <div className="border rounded-lg p-4 space-y-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <Inbox className="w-4 h-4 text-muted-foreground" aria-hidden="true" />
            <h3 className="text-sm font-semibold">{t('intake.title')}</h3>
            <Badge variant="outline" className="text-xs font-normal text-muted-foreground">
              {t('intake.comingSoon')}
            </Badge>
          </div>
          <p className="mt-1 text-xs text-muted-foreground">{t('intake.description')}</p>
        </div>
        <label className="flex items-center gap-2 select-none">
          <span className="text-sm font-medium">{t('intake.pause')}</span>
          <Switch checked={false} disabled aria-label={t('intake.pause')} />
        </label>
      </div>
      <p className="text-xs text-muted-foreground">{t('intake.pauseHint')}</p>

      {isLoading ? (
        <LoadingSkeleton variant="card" count={2} />
      ) : isError ? (
        <Alert variant="destructive">
          <AlertTriangle className="h-4 w-4" aria-hidden="true" />
          <AlertTitle>{t('intake.loadError')}</AlertTitle>
          <AlertDescription>
            <Button variant="outline" size="sm" onClick={() => refetch()}>
              {t('intake.retry')}
            </Button>
          </AlertDescription>
        </Alert>
      ) : sources.length === 0 ? (
        <EmptyState
          size="compact"
          icon={<Inbox className="h-8 w-8 text-muted-foreground" aria-hidden="true" />}
          title={t('intake.emptyTitle')}
          description={t('intake.emptyDescription')}
        />
      ) : (
        <>
          <h4 className="text-sm font-medium">{t('intake.sources')}</h4>
          <div className="grid gap-3 sm:grid-cols-2">
            {sources.map((source) => (
              <SourceCard key={source.source_kind} source={source} />
            ))}
          </div>
          <Alert variant="warning">
            <AlertTriangle className="h-4 w-4" aria-hidden="true" />
            <AlertDescription>{t('intake.calibrationHint')}</AlertDescription>
          </Alert>
        </>
      )}
    </div>
  )
}
