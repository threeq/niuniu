import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useBlocker } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { AlertTriangle, Inbox } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { EmptyState } from '@/components/shared/empty-state'
import { LoadingSkeleton } from '@/components/shared/loading-skeleton'
import { useExternalSources } from '@/hooks/useExternalSources'
import { useIsMobile } from '@/hooks/use-media-query'
import { confirm } from '@/lib/confirm'
import {
  intakeApi,
  type IntakeBindingKind,
  type IntakeRule,
  type IntakeSettingInput,
  type IntakeSettings,
  type IntakeSourceKind,
} from '@/lib/intake-api'
import { imbotApi } from '@/lib/imbot-api'
import { ConnectSourceDialog } from './connect-source-dialog'
import { PolicyHistoryDialog } from './policy-history-dialog'
import { RuleDialog } from './rule-dialog'
import { SourceCard } from './source-card'
import { SourceConfigDialog } from './source-config-dialog'
import {
  CONNECTABLE_SOURCES,
  bindingsByKind,
  changedInputs,
  draftFromSettings,
  hasChanges,
  knownActors as collectKnownActors,
  pendingCounts,
  validateDraft,
  type DraftMap,
  type DraftSetting,
} from './intake-utils'

interface Props {
  projectId: number
  // Lets the settings page guard section/tab switches while edits are pending.
  onDirtyChange?: (dirty: boolean) => void
  // Jump to the section that actually owns a source's connection setup.
  onNavigateSection?: (section: string) => void
}

interface RuleTarget {
  sourceKind: IntakeSourceKind
  rule: IntakeRule | null
}

export function IntakePolicyPanel({ projectId, onDirtyChange, onNavigateSection }: Props) {
  const { t } = useTranslation('intake')
  const qc = useQueryClient()
  const isMobile = useIsMobile()

  const [draft, setDraft] = useState<DraftMap | null>(null)
  const [configKind, setConfigKind] = useState<IntakeSourceKind | null>(null)
  const [connectKind, setConnectKind] = useState<IntakeBindingKind | null>(null)
  const [ruleTarget, setRuleTarget] = useState<RuleTarget | null>(null)
  const [historyKind, setHistoryKind] = useState<IntakeSourceKind | null>(null)

  const settingsQuery = useQuery({
    queryKey: ['intake-settings', projectId],
    queryFn: () => intakeApi.getSettings(projectId),
    retry: false,
  })
  const rulesQuery = useQuery({
    queryKey: ['intake-rules', projectId],
    queryFn: () => intakeApi.listRules(projectId),
    retry: false,
  })
  const decisionsQuery = useQuery({
    queryKey: ['intake-decisions-pending', projectId],
    queryFn: () => intakeApi.listDecisions(projectId, { executed: false, limit: 200 }),
    retry: false,
  })
  const calibrationQuery = useQuery({
    queryKey: ['intake-calibration', projectId],
    queryFn: () => intakeApi.getCalibration(projectId),
    retry: false,
  })
  const chatsQuery = useQuery({
    queryKey: ['imbot-chats', projectId],
    queryFn: () => imbotApi.listChats(projectId),
    retry: false,
  })
  const externalSourcesQuery = useExternalSources(projectId)
  // ci/email/webhook bindings own their own connection state: a card is
  // connected exactly when at least one binding of that kind exists.
  const sourcesQuery = useQuery({
    queryKey: ['intake-sources', projectId],
    queryFn: () => intakeApi.listSources(projectId),
    retry: false,
  })

  const settings = settingsQuery.data
  const rules = useMemo(() => rulesQuery.data ?? [], [rulesQuery.data])

  // Seed the draft once the settings load. Later refetches must not clobber
  // unsaved edits, so the draft is only (re)built explicitly on save success.
  useEffect(() => {
    if (settings && draft === null) setDraft(draftFromSettings(settings.settings))
  }, [settings, draft])

  const dirty = hasChanges(settings, draft)
  const errors = draft ? validateDraft(draft) : {}
  const invalid = Object.keys(errors).length > 0

  useEffect(() => {
    onDirtyChange?.(dirty)
  }, [dirty, onDirtyChange])
  useEffect(() => () => onDirtyChange?.(false), [onDirtyChange])

  // Browser close / reload.
  useEffect(() => {
    if (!dirty) return
    const handler = (e: BeforeUnloadEvent) => {
      e.preventDefault()
      e.returnValue = ''
    }
    window.addEventListener('beforeunload', handler)
    return () => window.removeEventListener('beforeunload', handler)
  }, [dirty])

  // Route changes.
  const blocker = useBlocker({
    shouldBlockFn: () => dirty,
    withResolver: true,
    enableBeforeUnload: false,
  })
  const blockerRef = useRef(blocker)
  blockerRef.current = blocker
  const blocked = blocker.status === 'blocked'
  useEffect(() => {
    if (!blocked) return
    let cancelled = false
    const current = blockerRef.current
    if (current.status !== 'blocked') return
    const { proceed, reset } = current
    void confirm({
      title: t('intake.unsavedLeaveTitle'),
      description: t('intake.unsavedLeaveDescription'),
      confirmText: t('intake.unsavedLeaveConfirm'),
      cancelText: t('intake.unsavedLeaveCancel'),
      destructive: true,
    }).then((ok) => {
      if (cancelled) return
      if (ok) proceed()
      else reset()
    })
    return () => {
      cancelled = true
    }
  }, [blocked, t])

  const saveMutation = useMutation({
    mutationFn: (rows: IntakeSettingInput[]) => intakeApi.updateSettings(projectId, rows),
    onSuccess: (data: IntakeSettings) => {
      setDraft(draftFromSettings(data.settings))
      qc.setQueryData(['intake-settings', projectId], data)
      void qc.invalidateQueries({ queryKey: ['intake-policy-versions', projectId] })
      toast.success(t('intake.saveSuccess'))
    },
    onError: (err: unknown) => {
      toast.error(
        t('intake.saveFailed', { message: err instanceof Error ? err.message : String(err) }),
      )
    },
  })

  const pauseMutation = useMutation({
    mutationFn: (paused: boolean) => intakeApi.setPaused(projectId, paused),
    onSuccess: (data) => {
      qc.setQueryData<IntakeSettings | undefined>(['intake-settings', projectId], (old) =>
        old ? { ...old, paused: data.paused } : old,
      )
      toast.success(data.paused ? t('intake.pausePausedToast') : t('intake.pauseResumedToast'))
    },
    onError: (err: unknown) => {
      toast.error(
        t('intake.pauseFailed', { message: err instanceof Error ? err.message : String(err) }),
      )
    },
  })

  const patchDraft = useCallback(
    (kind: string, patch: Partial<DraftSetting>) => {
      setDraft((prev) => {
        if (!prev) return prev
        const row = prev[kind]
        if (!row) return prev
        return { ...prev, [kind]: { ...row, ...patch } }
      })
    },
    [],
  )

  const sourcesByKind = useMemo(() => bindingsByKind(sourcesQuery.data), [sourcesQuery.data])

  const connected = useMemo(() => {
    const out: Record<string, boolean> = {}
    const chats = chatsQuery.data ?? []
    out.im_bot = chats.some((c) => c.status !== 'pending')
    const sources = externalSourcesQuery.data ?? []
    out.github = sources.some((s) => s.provider.toLowerCase().includes('github'))
    for (const kind of ['ci', 'email', 'webhook'] as const) {
      out[kind] = (sourcesByKind[kind] ?? []).length > 0
    }
    return out
  }, [chatsQuery.data, externalSourcesQuery.data, sourcesByKind])

  const pending = useMemo(() => pendingCounts(decisionsQuery.data), [decisionsQuery.data])
  const actors = useMemo(() => collectKnownActors(decisionsQuery.data), [decisionsQuery.data])

  const rulesByKind = useMemo(() => {
    const out: Record<string, IntakeRule[]> = {}
    for (const rule of rules) {
      ;(out[rule.source_kind] ??= []).push(rule)
    }
    return out
  }, [rules])

  const autoWithoutRules = useMemo(() => {
    if (!settings || !draft) return []
    return settings.settings
      .filter((s) => connected[s.source_kind] && draft[s.source_kind]?.mode === 'auto')
      .filter((s) => (rulesByKind[s.source_kind] ?? []).every((r) => !r.enabled))
      .map((s) => t(`intake.source.${s.source_kind}`))
  }, [settings, draft, connected, rulesByKind, t])

  // Auto selected before the sample is large/good enough: warn, never block
  // (design §6.7.3). Also drives the progress hint below.
  const calibration = calibrationQuery.data
  const autoKinds = useMemo(() => {
    if (!settings || !draft) return [] as string[]
    return settings.settings
      .filter((s) => draft[s.source_kind]?.mode === 'auto')
      .map((s) => t(`intake.source.${s.source_kind}`))
  }, [settings, draft, t])
  const autoBelowCalibration =
    autoKinds.length > 0 && calibration !== undefined && !calibration.ready

  const canSave = dirty && !invalid && !saveMutation.isPending
  const paused = settings?.paused ?? false

  const save = () => {
    if (!settings || !draft || !canSave) return
    const rows = changedInputs(settings.settings, draft)
    if (rows.length === 0) return
    saveMutation.mutate(rows)
  }

  const changeEnabled = (kind: string, enabled: boolean) => patchDraft(kind, { enabled })

  const connectSource = (kind: IntakeSourceKind) => {
    const target = CONNECTABLE_SOURCES[kind]
    if (!target) return
    if (target === 'inline') {
      setConnectKind(kind as IntakeBindingKind)
      return
    }
    onNavigateSection?.(target)
  }

  const configSource = configKind ? settings?.settings.find((s) => s.source_kind === configKind) ?? null : null
  const configDraft = configKind && draft ? draft[configKind] ?? null : null
  const configRules = configKind ? rulesByKind[configKind] ?? [] : []

  return (
    <div className="space-y-4 rounded-lg border p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <Inbox className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
            <h3 className="text-sm font-semibold">{t('intake.title')}</h3>
            {dirty ? (
              <Badge variant="outline" className="text-xs font-normal text-warm-text-muted">
                {t('intake.unsaved')}
              </Badge>
            ) : null}
          </div>
          <p className="mt-1 text-xs text-muted-foreground">{t('intake.description')}</p>
        </div>
        <div className="flex items-center gap-3">
          <label className="flex select-none items-center gap-2">
            <span className="text-sm font-medium">{t('intake.pause')}</span>
            <Switch
              checked={paused}
              onCheckedChange={(next) => pauseMutation.mutate(next)}
              disabled={pauseMutation.isPending || !settings}
              aria-label={t('intake.pause')}
            />
          </label>
          <Button type="button" onClick={save} disabled={!canSave}>
            {saveMutation.isPending ? t('intake.saving') : t('intake.save')}
          </Button>
        </div>
      </div>
      <p className="text-xs text-muted-foreground">{t('intake.pauseHint')}</p>
      {invalid ? <p className="text-xs text-destructive-text">{t('intake.saveInvalid')}</p> : null}

      {settingsQuery.isLoading ? (
        <LoadingSkeleton variant="card" count={2} />
      ) : settingsQuery.isError ? (
        <Alert variant="destructive">
          <AlertTriangle className="h-4 w-4" aria-hidden="true" />
          <AlertTitle>{t('intake.loadError')}</AlertTitle>
          <AlertDescription>
            <Button type="button" variant="outline" size="sm" onClick={() => settingsQuery.refetch()}>
              {t('intake.retry')}
            </Button>
          </AlertDescription>
        </Alert>
      ) : !settings || !draft ? (
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
            {settings.settings.map((source) => {
              const row = draft[source.source_kind]
              if (!row) return null
              return (
                <SourceCard
                  key={source.source_kind}
                  projectId={projectId}
                  source={source}
                  draft={row}
                  connected={connected[source.source_kind] ?? false}
                  canConnect={Boolean(CONNECTABLE_SOURCES[source.source_kind])}
                  bindings={sourcesByKind[source.source_kind] ?? []}
                  pending={pending[source.source_kind] ?? 0}
                  ruleCount={(rulesByKind[source.source_kind] ?? []).length}
                  onEnabledChange={(enabled) => changeEnabled(source.source_kind, enabled)}
                  onOpenConfig={() => setConfigKind(source.source_kind)}
                  onConnect={() => connectSource(source.source_kind)}
                />
              )
            })}
          </div>

          {autoWithoutRules.length > 0 ? (
            <Alert variant="warning">
              <AlertTriangle className="h-4 w-4" aria-hidden="true" />
              <AlertDescription>
                {t('intake.autoNoRules', { sources: autoWithoutRules.join(', ') })}
              </AlertDescription>
            </Alert>
          ) : null}

          {autoBelowCalibration && calibration ? (
            <Alert variant="warning">
              <AlertTriangle className="h-4 w-4" aria-hidden="true" />
              <AlertDescription>
                {t('intake.autoNeedsCalibration', {
                  sources: autoKinds.join(', '),
                  calibrated: calibration.calibrated,
                  min: calibration.min_calibrated,
                  rate: Math.round(calibration.acceptance_rate * 100),
                })}
              </AlertDescription>
            </Alert>
          ) : calibration ? (
            !calibration.ready ? (
              <p className="text-xs text-muted-foreground tabular-nums">
                {t('intake.calibrationProgress', {
                  count: calibration.calibrated,
                  min: calibration.min_calibrated,
                  rate: Math.round(calibration.acceptance_rate * 100),
                })}
              </p>
            ) : null
          ) : (
            <Alert variant="warning">
              <AlertTriangle className="h-4 w-4" aria-hidden="true" />
              <AlertDescription>{t('intake.calibrationHint')}</AlertDescription>
            </Alert>
          )}
        </>
      )}

      <ConnectSourceDialog
        projectId={projectId}
        sourceKind={connectKind}
        open={connectKind !== null}
        onOpenChange={(open) => {
          if (!open) setConnectKind(null)
        }}
        onConnected={() => {
          void qc.invalidateQueries({ queryKey: ['intake-sources', projectId] })
        }}
      />

      <SourceConfigDialog
        projectId={projectId}
        source={configSource}
        draft={configDraft}
        open={configKind !== null}
        onOpenChange={(open) => {
          if (!open) setConfigKind(null)
        }}
        onDraftChange={(patch) => {
          if (configKind) patchDraft(configKind, patch)
        }}
        rules={configRules}
        rulesLoading={rulesQuery.isLoading}
        knownActors={actors}
        paused={paused}
        pausePending={pauseMutation.isPending}
        onPausedChange={(next) => pauseMutation.mutate(next)}
        onEditRule={(rule) => setRuleTarget({ sourceKind: rule.source_kind, rule })}
        onCreateRule={() => {
          if (configKind) setRuleTarget({ sourceKind: configKind, rule: null })
        }}
        onOpenHistory={() => {
          if (configKind) {
            setHistoryKind(configKind)
            setConfigKind(null)
          }
        }}
        isMobile={isMobile}
      />

      <RuleDialog
        key={`${ruleTarget?.sourceKind ?? 'closed'}-${ruleTarget?.rule?.id ?? 'new'}`}
        projectId={projectId}
        sourceKind={ruleTarget?.sourceKind ?? 'im_bot'}
        rule={ruleTarget?.rule ?? null}
        open={ruleTarget !== null}
        onOpenChange={(open) => {
          if (!open) setRuleTarget(null)
        }}
        actors={actors}
      />

      <PolicyHistoryDialog
        key={historyKind ?? 'closed'}
        projectId={projectId}
        sourceKind={historyKind}
        open={historyKind !== null}
        onOpenChange={(open) => {
          if (!open) setHistoryKind(null)
        }}
      />
    </div>
  )
}
