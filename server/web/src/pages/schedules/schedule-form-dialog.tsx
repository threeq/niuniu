import { useState, useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { RefreshCw, Pin } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { WorkspaceSchedule } from '@/types/api'
import { useScheduleMutations } from '@/lib/hooks/use-schedule-mutations'

interface ScheduleFormDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  workspaceId: string | number
  schedule?: WorkspaceSchedule // undefined = create mode, defined = edit mode
}

export function ScheduleFormDialog({
  open,
  onOpenChange,
  workspaceId,
  schedule,
}: ScheduleFormDialogProps) {
  const { t } = useTranslation('schedules')
  const isEdit = !!schedule
  const { create, update } = useScheduleMutations(workspaceId)

  const cronPresets = useMemo(
    () => [
      { label: t('form.presetDaily09'), expr: '0 9 * * *' },
      { label: t('form.presetDaily14'), expr: '0 14 * * *' },
      { label: t('form.presetWeekday09'), expr: '0 9 * * 1-5' },
      { label: t('form.presetWeekday14'), expr: '0 14 * * 1-5' },
      { label: t('form.presetEvery2h'), expr: '0 */2 * * *' },
      { label: t('form.presetEvery4h'), expr: '0 */4 * * *' },
    ],
    [t],
  )

  const [name, setName] = useState('')
  const [scheduleType, setScheduleType] = useState<'cron' | 'once'>('cron')
  const [actionKind, setActionKind] = useState<'agent_message' | 'autonomous_discovery'>('agent_message')
  const [cronExpr, setCronExpr] = useState('')
  const [runAt, setRunAt] = useState('')
  const [defaultMessage, setDefaultMessage] = useState('')
  const [error, setError] = useState('')

  // Compare-during-render: reset form fields when schedule prop or open changes
  const [prevResetKey, setPrevResetKey] = useState<string | undefined>(undefined)
  const resetKey = `${open}:${schedule?.id ?? 'new'}:${schedule?.updated_at ?? ''}`
  if (resetKey !== prevResetKey) {
    setPrevResetKey(resetKey)
    if (schedule) {
      setName(schedule.name)
      setDefaultMessage(schedule.default_message || '')
      setScheduleType(schedule.schedule_type)
      setActionKind(schedule.action_kind === 'autonomous_discovery' ? 'autonomous_discovery' : 'agent_message')
      setCronExpr(schedule.cron_expr)
      setRunAt(schedule.run_at ? new Date(schedule.run_at).toISOString().slice(0, 16) : '')
    } else {
      setName('')
      setDefaultMessage('')
      setScheduleType('cron')
      setActionKind('agent_message')
      setCronExpr('')
      setRunAt('')
    }
    setError('')
  }

  const handleSubmit = async () => {
    setError('')
    const data = {
      name: name.trim() || (isEdit ? schedule!.name : undefined),
      default_message: defaultMessage.trim(),
      schedule_type: scheduleType,
      action_kind: actionKind,
      cron_expr: scheduleType === 'cron' ? cronExpr.trim() : undefined,
      run_at: scheduleType === 'once' ? new Date(runAt).toISOString() : undefined,
    }

    try {
      if (isEdit) {
        await update.mutateAsync({
          scheduleId: schedule!.id,
          data: { name: name.trim() || schedule!.name, default_message: defaultMessage.trim(), schedule_type: scheduleType, action_kind: actionKind, cron_expr: data.cron_expr, run_at: data.run_at },
        })
      } else {
        await create.mutateAsync(data)
      }
      onOpenChange(false)
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common:status.saveFailed'))
    }
  }

  const canSubmit =
    (scheduleType === 'cron' && cronExpr.trim()) ||
    (scheduleType === 'once' && runAt)

  if (!open) return null

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      <div className="fixed inset-0 bg-black/50" onClick={() => onOpenChange(false)} />
      <div className="relative bg-card rounded-lg shadow-lg w-full max-w-md p-6 space-y-4">
        <h3 className="text-base font-semibold text-foreground">
          {isEdit ? t('form.titleEdit') : t('form.titleAdd')}
        </h3>

        {error && (
          <p className="text-xs text-destructive bg-destructive/10 dark:bg-red-950/30 dark:text-red-300 rounded px-2.5 py-1.5">{error}</p>
        )}

        {/* Name */}
        <div>
          <label className="block text-xs font-medium text-muted-foreground mb-1">{t('form.nameLabel')}</label>
          <input
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t('form.namePlaceholder')}
            className="w-full rounded border border-border bg-background px-2.5 py-1.5 text-sm focus:outline-none focus:ring-1 focus:ring-ring"
          />
        </div>

        {/* Type toggle */}
        <div className="flex gap-2">
          <button
            onClick={() => setScheduleType('cron')}
            className={cn(
              'rounded px-3 py-1.5 text-xs transition-colors',
              scheduleType === 'cron'
                ? 'bg-info text-white'
                : 'bg-muted text-muted-foreground hover:bg-accent'
            )}
          >
            <RefreshCw className="inline h-3 w-3 mr-1 -mt-0.5" />
            {t('form.typeCron')}
          </button>
          <button
            onClick={() => setScheduleType('once')}
            className={cn(
              'rounded px-3 py-1.5 text-xs transition-colors',
              scheduleType === 'once'
                ? 'bg-info text-white'
                : 'bg-muted text-muted-foreground hover:bg-accent'
            )}
          >
            <Pin className="inline h-3 w-3 mr-1 -mt-0.5" />
            {t('form.typeOnce')}
          </button>
        </div>

        {/* Cron input + presets */}
        {scheduleType === 'cron' && (
          <div>
            <label className="block text-xs font-medium text-muted-foreground mb-1">{t('form.cronLabel')}</label>
            <input
              type="text"
              value={cronExpr}
              onChange={(e) => setCronExpr(e.target.value)}
              placeholder={t('form.cronPlaceholder')}
              className="w-full rounded border border-border bg-background px-2.5 py-1.5 text-sm font-mono focus:outline-none focus:ring-1 focus:ring-ring mb-2"
            />
            <div className="flex flex-wrap gap-1">
              {cronPresets.map((preset) => (
                <button
                  key={preset.expr}
                  onClick={() => setCronExpr(preset.expr)}
                  className={cn(
                    'rounded px-2 py-0.5 text-xs transition-colors',
                    cronExpr === preset.expr
                      ? 'bg-info/10 text-info border border-info/30'
                      : 'bg-muted text-muted-foreground hover:bg-accent'
                  )}
                >
                  {preset.label}
                </button>
              ))}
            </div>
          </div>
        )}

        {/* Once datetime picker */}
        {scheduleType === 'once' && (
          <div>
            <label className="block text-xs font-medium text-muted-foreground mb-1">{t('form.runAtLabel')}</label>
            <input
              type="datetime-local"
              value={runAt}
              onChange={(e) => setRunAt(e.target.value)}
              className="w-full rounded border border-border bg-background px-2.5 py-1.5 text-sm focus:outline-none focus:ring-1 focus:ring-ring"
            />
          </div>
        )}

        {/* Default message */}
        <div>
          <label className="block text-xs font-medium text-muted-foreground mb-1">{t('form.defaultMessageLabel')}</label>
          <textarea
            value={defaultMessage}
            onChange={(e) => setDefaultMessage(e.target.value)}
            placeholder={t('form.defaultMessagePlaceholder')}
            rows={3}
            className="w-full rounded border border-border bg-background px-2.5 py-1.5 text-sm focus:outline-none focus:ring-1 focus:ring-ring resize-none"
          />
          <p className="text-xs text-muted-foreground/70 mt-0.5">{t('form.defaultMessageHint')}</p>
        </div>

        {/* Action kind */}
        <div>
          <label className="block text-xs font-medium text-muted-foreground mb-1">{t('form.actionKindLabel')}</label>
          <div className="flex gap-2">
            <button
              onClick={() => setActionKind('agent_message')}
              className={cn(
                'rounded px-3 py-1.5 text-xs transition-colors',
                actionKind === 'agent_message'
                  ? 'bg-info text-white'
                  : 'bg-muted text-muted-foreground hover:bg-accent'
              )}
            >
              {t('form.actionKindMessage')}
            </button>
            <button
              onClick={() => setActionKind('autonomous_discovery')}
              className={cn(
                'rounded px-3 py-1.5 text-xs transition-colors',
                actionKind === 'autonomous_discovery'
                  ? 'bg-info text-white'
                  : 'bg-muted text-muted-foreground hover:bg-accent'
              )}
            >
              {t('form.actionKindDiscovery')}
            </button>
          </div>
          <p className="text-xs text-muted-foreground/70 mt-0.5">
            {actionKind === 'autonomous_discovery' ? t('form.actionKindDiscoveryHint') : t('form.actionKindMessageHint')}
          </p>
        </div>

        {/* Actions */}
        <div className="flex justify-end gap-2 pt-2">
          <button
            onClick={() => onOpenChange(false)}
            className="rounded px-3 py-1.5 text-xs text-muted-foreground hover:bg-accent"
          >
            {t('common:actions.cancel')}
          </button>
          <button
            onClick={handleSubmit}
            disabled={!canSubmit || create.isPending || update.isPending}
            className="rounded bg-info px-3 py-1.5 text-xs text-white hover:bg-info/90 disabled:opacity-50"
          >
            {create.isPending || update.isPending
              ? t('common:actions.saving')
              : isEdit ? t('common:actions.save') : t('form.submitAdd')}
          </button>
        </div>
      </div>
    </div>
  )
}
