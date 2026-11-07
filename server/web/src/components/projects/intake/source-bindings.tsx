import { useMemo, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { AlertTriangle, KeyRound, Play, RefreshCw, Trash2 } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/shared/empty-state'
import { confirm } from '@/lib/confirm'
import {
  intakeApi,
  type IntakeSource,
  type IntakeSourceWriteResult,
  type IntakeTestResult,
} from '@/lib/intake-api'
import { cn } from '@/lib/utils'
import {
  bindingStatus,
  classifyInboundOrigin,
  isPushBinding,
  type BindingStatus,
} from './intake-utils'

const STATUS_CLASS: Record<BindingStatus, string> = {
  ok: 'border-success/30 bg-success/15 text-success',
  degraded: 'border-warning/40 bg-warning/15 text-warning',
  error: 'border-destructive/30 bg-destructive/10 text-destructive-text',
  never: 'border-warm-border bg-warm-muted text-warm-text-muted',
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

interface Props {
  projectId: number
  bindings: IntakeSource[]
  loading?: boolean
}

/** One binding's inbound address, its delivery health and the actions that
 *  operate on it (design §11.3). Secrets never render here: rotating swaps to
 *  a one-time link instead. */
export function SourceBindings({ projectId, bindings, loading = false }: Props) {
  const { t } = useTranslation('intake')
  const qc = useQueryClient()

  const [expanded, setExpanded] = useState<number | null>(null)
  const [testResults, setTestResults] = useState<Record<number, IntakeTestResult>>({})
  const [rotated, setRotated] = useState<IntakeSourceWriteResult | null>(null)

  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: ['intake-sources', projectId] })
  }

  const testMutation = useMutation({
    mutationFn: (source: IntakeSource) => intakeApi.testSource(projectId, source.id),
    onSuccess: (result, source) => {
      setTestResults((prev) => ({ ...prev, [source.id]: result }))
    },
    onError: (err: unknown) => {
      toast.error(t('intake.bindings.testFailed', { message: errorMessage(err) }))
    },
  })

  const rotateMutation = useMutation({
    mutationFn: (source: IntakeSource) => intakeApi.rotateSource(projectId, source.id),
    onSuccess: (result) => {
      setRotated(result)
      invalidate()
    },
    onError: (err: unknown) => {
      toast.error(t('intake.bindings.rotateFailed', { message: errorMessage(err) }))
    },
  })

  const pollMutation = useMutation({
    mutationFn: (source: IntakeSource) => intakeApi.pollSource(projectId, source.id),
    onSuccess: () => {
      toast.success(t('intake.bindings.pollStarted'))
      invalidate()
    },
    onError: (err: unknown) => {
      toast.error(t('intake.bindings.pollFailed', { message: errorMessage(err) }))
    },
  })

  const deleteMutation = useMutation({
    mutationFn: (source: IntakeSource) => intakeApi.deleteSource(projectId, source.id),
    onSuccess: () => {
      toast.success(t('intake.bindings.deleted'))
      invalidate()
    },
    onError: (err: unknown) => {
      toast.error(t('intake.bindings.deleteFailed', { message: errorMessage(err) }))
    },
  })

  const hasPush = useMemo(() => bindings.some(isPushBinding), [bindings])

  if (loading) {
    return <p className="py-2 text-center text-xs text-muted-foreground">{t('intake.loading')}</p>
  }
  if (bindings.length === 0) {
    return (
      <EmptyState
        size="compact"
        title={t('intake.bindings.emptyTitle')}
        description={t('intake.bindings.empty')}
      />
    )
  }

  return (
    <div className="space-y-3">
      {hasPush ? <ReachabilityHint /> : null}

      {bindings.map((binding) => {
        const status = bindingStatus(binding)
        const push = isPushBinding(binding)
        const imap = binding.source_kind === 'email' && binding.mode === 'imap'
        const testResult = testResults[binding.id]
        const rotateResult = rotated?.source.id === binding.id ? rotated : null
        const expandedError = expanded === binding.id
        const hasErrorDetail = Boolean(binding.last_error || binding.backoff_until)
        const providerLabel = binding.provider
          ? t(
              `intake.connectDialog.${binding.source_kind === 'email' ? 'emailProviderName' : 'ciProviderName'}.${binding.provider}`,
              { defaultValue: binding.provider },
            )
          : t('intake.bindings.genericProvider')

        return (
          <div key={binding.id} className="space-y-3 rounded-md border border-warm-border p-3">
            <div className="flex flex-wrap items-start justify-between gap-2">
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="truncate text-sm font-medium">{binding.name}</span>
                  <Badge
                    variant="outline"
                    className={cn(
                      'rounded px-1.5 py-0.5 text-xs',
                      STATUS_CLASS[status],
                    )}
                  >
                    {t(`intake.bindings.status.${status}`)}
                  </Badge>
                </div>
                <p className="mt-1 text-xs text-muted-foreground">
                  {t('intake.bindings.meta', {
                    mode: t(`intake.bindings.mode.${binding.mode || 'push'}`, {
                      defaultValue: binding.mode || 'push',
                    }),
                    provider: providerLabel,
                  })}
                </p>
              </div>

              <div className="flex flex-wrap items-center gap-1">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => testMutation.mutate(binding)}
                  disabled={testMutation.isPending}
                >
                  <Play className="mr-1 h-4 w-4" aria-hidden="true" />
                  {testMutation.isPending && testMutation.variables?.id === binding.id
                    ? t('intake.bindings.testing')
                    : t('intake.bindings.test')}
                </Button>
                {push ? (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={async () => {
                      if (
                        await confirm({
                          description: t('intake.bindings.rotateConfirm', { name: binding.name }),
                          destructive: true,
                        })
                      ) {
                        rotateMutation.mutate(binding)
                      }
                    }}
                    disabled={rotateMutation.isPending}
                  >
                    <KeyRound className="mr-1 h-4 w-4" aria-hidden="true" />
                    {t('intake.bindings.rotate')}
                  </Button>
                ) : null}
                {imap ? (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() => pollMutation.mutate(binding)}
                    disabled={pollMutation.isPending}
                  >
                    <RefreshCw className="mr-1 h-4 w-4" aria-hidden="true" />
                    {pollMutation.isPending && pollMutation.variables?.id === binding.id
                      ? t('intake.bindings.polling')
                      : t('intake.bindings.poll')}
                  </Button>
                ) : null}
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="text-destructive-text hover:bg-destructive/10 hover:text-destructive-text"
                  aria-label={t('intake.bindings.deleteNamed', { name: binding.name })}
                  onClick={async () => {
                    if (
                      await confirm({
                        description: t('intake.bindings.deleteConfirm', { name: binding.name }),
                        destructive: true,
                      })
                    ) {
                      deleteMutation.mutate(binding)
                    }
                  }}
                  disabled={deleteMutation.isPending}
                >
                  <Trash2 className="h-4 w-4" aria-hidden="true" />
                </Button>
              </div>
            </div>

            <dl className="grid gap-x-4 gap-y-1 text-xs sm:grid-cols-2">
              <div className="flex gap-2">
                <dt className="shrink-0 text-muted-foreground">{t('intake.bindings.lastDelivery')}</dt>
                <dd>{binding.last_event_at ? new Date(binding.last_event_at).toLocaleString() : t('intake.bindings.never')}</dd>
              </div>
              <div className="flex gap-2">
                <dt className="shrink-0 text-muted-foreground">{t('intake.bindings.events24h')}</dt>
                <dd className="tabular-nums">{binding.events_24h}</dd>
              </div>
              {push ? (
                <div className="flex gap-2 sm:col-span-2">
                  <dt className="shrink-0 text-muted-foreground">{t('intake.bindings.inboundUrl')}</dt>
                  <dd className="min-w-0 break-all font-mono">
                    {window.location.origin}
                    {binding.inbound_path}
                  </dd>
                </div>
              ) : null}
            </dl>

            {hasErrorDetail ? (
              <div className="space-y-1">
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="h-auto px-0 text-xs text-muted-foreground hover:bg-transparent"
                  aria-expanded={expandedError}
                  onClick={() => setExpanded(expandedError ? null : binding.id)}
                >
                  {expandedError
                    ? t('intake.bindings.hideError')
                    : t('intake.bindings.showError')}
                </Button>
                {expandedError ? (
                  <div className="space-y-1 rounded-md bg-warm-muted p-2 text-xs">
                    {binding.last_error ? (
                      <p className="break-words text-destructive-text">{binding.last_error}</p>
                    ) : null}
                    {binding.backoff_until ? (
                      <p className="text-muted-foreground">
                        {t('intake.bindings.backoffUntil', {
                          time: new Date(binding.backoff_until).toLocaleString(),
                        })}
                      </p>
                    ) : null}
                  </div>
                ) : null}
              </div>
            ) : null}

            {testResult ? (
              <div className="flex flex-wrap items-center gap-2 rounded-md border border-warm-border bg-warm-muted p-2 text-xs">
                <Badge
                  variant="outline"
                  className={cn(
                    'rounded px-1.5 py-0.5 text-xs',
                    testResult.allowed
                      ? 'border-success/30 bg-success/15 text-success'
                      : 'border-warm-border bg-warm-muted text-warm-text-muted',
                  )}
                >
                  {testResult.allowed
                    ? t('intake.bindings.testAllowed')
                    : t('intake.bindings.testDenied')}
                </Badge>
                <span className="text-muted-foreground">
                  {t('intake.bindings.testSummary', {
                    event: testResult.event_kind,
                    reason: testResult.reason_code,
                  })}
                </span>
              </div>
            ) : null}

            {rotateResult ? (
              rotateResult.secret_link ? (
                <div className="space-y-1 rounded-md border border-warm-border bg-warm-muted p-2">
                  <p className="text-xs font-medium">{t('intake.bindings.rotateSecretTitle')}</p>
                  <p className="text-xs text-muted-foreground">
                    {t('intake.bindings.rotateSecretHint')}
                  </p>
                  <Button type="button" variant="outline" size="sm" asChild>
                    <a href={rotateResult.secret_link} target="_blank" rel="noopener noreferrer">
                      {t('intake.bindings.rotateOpenSecret')}
                    </a>
                  </Button>
                </div>
              ) : (
                <p className="text-xs text-muted-foreground">{t('intake.bindings.rotateNoSecret')}</p>
              )
            ) : null}
          </div>
        )
      })}

      {hasPush ? (
        <p className="text-xs text-muted-foreground">{t('intake.bindings.pushHowTo')}</p>
      ) : null}
    </div>
  )
}

/** Reachability of the generated inbound address, judged from the origin the
 *  user is browsing on (design §11.4). Never blocks creating a binding. */
export function ReachabilityHint({ origin }: { origin?: string }) {
  const { t } = useTranslation('intake')
  const currentOrigin = origin ?? window.location.origin
  const level = classifyInboundOrigin(currentOrigin)
  const warn = level !== 'public'

  return (
    <div
      className={cn(
        'flex items-start gap-2 rounded-md border p-3 text-xs',
        warn
          ? 'border-warning/40 bg-warning/10'
          : 'border-warm-border bg-warm-muted',
      )}
    >
      <AlertTriangle
        className={cn('mt-0.5 h-4 w-4 shrink-0', warn ? 'text-warning' : 'text-muted-foreground')}
        aria-hidden="true"
      />
      <div className="space-y-1">
        <p className="text-sm font-medium">{t('intake.bindings.reachability.title')}</p>
        <p className="text-muted-foreground">
          {t(`intake.bindings.reachability.${level}`, { origin: currentOrigin })}
        </p>
        {warn ? (
          <p className="text-muted-foreground">{t('intake.bindings.reachability.options')}</p>
        ) : null}
      </div>
    </div>
  )
}
