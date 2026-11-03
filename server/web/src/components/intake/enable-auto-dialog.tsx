import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, Zap } from 'lucide-react'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Checkbox } from '@/components/ui/checkbox'
import type {
  IntakeCalibration,
  IntakeCalibrationSource,
  IntakeRule,
  IntakeSetting,
  IntakeSettings,
} from '@/lib/intake-api'
import { useRelativeTime } from './use-relative-time'

interface EnableAutoDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  calibration: IntakeCalibration
  settings: IntakeSettings | undefined
  rules: IntakeRule[]
  pending: boolean
  onConfirm: (sourceKinds: string[]) => void
}

function percent(rate: number) {
  return Math.round(rate * 100)
}

/**
 * First-time auto confirmation (design §6.7.9 step 3). It shows exactly what
 * changes: which sources, their allow list, enabled rules and daily cap, plus
 * the calibration sample that earned the recommendation. Pausing is always
 * available and resuming never asks for another calibration.
 */
export function EnableAutoDialog({
  open,
  onOpenChange,
  calibration,
  settings,
  rules,
  pending,
  onConfirm,
}: EnableAutoDialogProps) {
  const { t } = useTranslation('intake')
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('calibration.dialog.title')}</DialogTitle>
          <DialogDescription>{t('calibration.dialog.description')}</DialogDescription>
        </DialogHeader>
        {open ? (
          <EnableAutoForm
            calibration={calibration}
            settings={settings}
            rules={rules}
            pending={pending}
            onCancel={() => onOpenChange(false)}
            onConfirm={onConfirm}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  )
}

function EnableAutoForm({
  calibration,
  settings,
  rules,
  pending,
  onCancel,
  onConfirm,
}: {
  calibration: IntakeCalibration
  settings: IntakeSettings | undefined
  rules: IntakeRule[]
  pending: boolean
  onCancel: () => void
  onConfirm: (sourceKinds: string[]) => void
}) {
  const { t } = useTranslation('intake')
  const relative = useRelativeTime()
  const sources = calibration.sources.filter((source) => source.calibrated > 0)
  const [selected, setSelected] = useState<Set<string>>(
    () => new Set(sources.map((source) => source.source_kind)),
  )
  const [error, setError] = useState('')

  const settingByKind = new Map<string, IntakeSetting>(
    (settings?.settings ?? []).map((s) => [s.source_kind, s]),
  )
  const rulesByKind = new Map<string, IntakeRule[]>()
  for (const rule of rules) {
    const list = rulesByKind.get(rule.source_kind) ?? []
    list.push(rule)
    rulesByKind.set(rule.source_kind, list)
  }

  function toggle(kind: string, checked: boolean) {
    setSelected((current) => {
      const next = new Set(current)
      if (checked) next.add(kind)
      else next.delete(kind)
      return next
    })
  }

  function submit() {
    if (selected.size === 0) {
      setError(t('calibration.dialog.noSelection'))
      return
    }
    onConfirm([...selected])
  }

  return (
    <div className="space-y-4">
      <section className="rounded-md border border-warm-border bg-warm-surface px-3 py-2">
        <p className="text-sm font-medium tabular-nums">
          {t('calibration.summary', {
            count: calibration.calibrated,
            rate: percent(calibration.acceptance_rate),
          })}
        </p>
        <p className="mt-0.5 text-xs text-warm-text-muted tabular-nums">
          {t('calibration.dialog.sample', {
            calibrated: calibration.calibrated,
            accepted: calibration.accepted,
            ignored: calibration.ignored,
            rate: percent(calibration.acceptance_rate),
          })}
        </p>
      </section>

      <section className="space-y-2">
        <h4 className="text-sm font-medium">{t('calibration.dialog.sourcesTitle')}</h4>
        <div className="space-y-2">
          {sources.map((source) => (
            <SourceRow
              key={source.source_kind}
              source={source}
              setting={settingByKind.get(source.source_kind)}
              rules={(rulesByKind.get(source.source_kind) ?? []).filter((rule) => rule.enabled)}
              checked={selected.has(source.source_kind)}
              onCheckedChange={(checked) => toggle(source.source_kind, checked)}
            />
          ))}
        </div>
      </section>

      <section className="space-y-1.5">
        <h4 className="text-sm font-medium">{t('calibration.dialog.recentTitle')}</h4>
        {calibration.recent.length === 0 ? (
          <p className="text-xs text-warm-text-muted">{t('calibration.dialog.recentEmpty')}</p>
        ) : (
          <ul className="max-h-40 space-y-1 overflow-y-auto">
            {calibration.recent.map((record) => (
              <li
                key={record.id}
                className="flex items-center gap-2 text-xs text-warm-text-muted"
              >
                <Badge
                  variant="outline"
                  className="text-xs font-normal"
                >
                  {record.outcome === 'accepted'
                    ? t('calibration.dialog.outcomeAccepted')
                    : t('calibration.dialog.outcomeIgnored')}
                </Badge>
                <span className="truncate">
                  {t(`intake.source.${record.source_kind}`, {
                    defaultValue: record.source_kind,
                  })}
                  {' · '}
                  {record.actor_name || record.actor_ext_id}
                </span>
                <span className="ml-auto shrink-0">{relative(record.created_at)}</span>
              </li>
            ))}
          </ul>
        )}
      </section>

      <p className="text-xs text-warm-text-muted">{t('calibration.dialog.pauseNote')}</p>
      {error ? <p className="text-xs text-destructive">{error}</p> : null}

      <DialogFooter>
        <Button type="button" variant="ghost" onClick={onCancel} disabled={pending}>
          {t('calibration.dialog.cancel')}
        </Button>
        <Button type="button" onClick={submit} disabled={pending || selected.size === 0}>
          <Zap className="mr-1.5 h-4 w-4" aria-hidden="true" />
          {pending ? t('calibration.dialog.confirming') : t('calibration.dialog.confirm')}
        </Button>
      </DialogFooter>
    </div>
  )
}

function SourceRow({
  source,
  setting,
  rules,
  checked,
  onCheckedChange,
}: {
  source: IntakeCalibrationSource
  setting: IntakeSetting | undefined
  rules: IntakeRule[]
  checked: boolean
  onCheckedChange: (checked: boolean) => void
}) {
  const { t } = useTranslation('intake')
  const sourceName = t(`intake.source.${source.source_kind}`, {
    defaultValue: source.source_kind,
  })

  return (
    <div className="rounded-md border border-warm-border p-3">
      <label className="flex items-start gap-2.5">
        <Checkbox
          checked={checked}
          onCheckedChange={(value) => onCheckedChange(value === true)}
          aria-label={sourceName}
          className="mt-0.5"
        />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="text-sm font-medium">{sourceName}</span>
            {setting ? (
              <Badge variant="outline" className="text-xs font-normal">
                {t(`intake.mode.${setting.mode}`, { defaultValue: setting.mode })}
              </Badge>
            ) : null}
            <span className="text-xs text-warm-text-muted tabular-nums">
              {t('calibration.dialog.sourceStats', {
                calibrated: source.calibrated,
                rate: percent(source.acceptance_rate),
              })}
            </span>
          </div>
          {setting ? (
            <>
              <p className="mt-1 text-xs text-warm-text-muted tabular-nums">
                {t('calibration.dialog.sourceDetail', {
                  cap: setting.daily_cap,
                  rules: rules.length,
                  members: setting.allow_list.length,
                })}
              </p>
              <p className="mt-0.5 text-xs text-warm-text-muted">
                {setting.allow_list.length > 0
                  ? t('calibration.dialog.allowList', { list: setting.allow_list.join(', ') })
                  : t('calibration.dialog.allowListEmpty')}
              </p>
              <p className="mt-0.5 text-xs text-warm-text-muted">
                {rules.length > 0
                  ? t('calibration.dialog.rules', {
                      list: rules.map((rule) => rule.name).join(', '),
                    })
                  : t('calibration.dialog.rulesEmpty')}
              </p>
              {rules.length === 0 ? (
                <p className="mt-1 flex items-center gap-1 text-xs text-destructive">
                  <AlertTriangle className="h-3.5 w-3.5" aria-hidden="true" />
                  {t('calibration.dialog.noRules')}
                </p>
              ) : null}
              {setting.daily_cap === 0 ? (
                <p className="mt-1 flex items-center gap-1 text-xs text-destructive">
                  <AlertTriangle className="h-3.5 w-3.5" aria-hidden="true" />
                  {t('calibration.dialog.zeroCap')}
                </p>
              ) : null}
            </>
          ) : (
            <p className="mt-1 text-xs text-warm-text-muted">
              {t('calibration.dialog.settingsMissing')}
            </p>
          )}
        </div>
      </label>
    </div>
  )
}
