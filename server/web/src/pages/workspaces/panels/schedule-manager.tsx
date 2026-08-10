import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Clock, Plus, Trash2, RefreshCw, Pin } from 'lucide-react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { cn } from '@/lib/utils'
import type { WorkspaceSchedule } from '@/types/api'

interface ScheduleManagerProps {
  workspaceId: string
}

const CRON_PRESETS = [
  { key: 'daily9', expr: '0 9 * * *' },
  { key: 'daily14', expr: '0 14 * * *' },
  { key: 'weekday9', expr: '0 9 * * 1-5' },
  { key: 'weekday14', expr: '0 14 * * 1-5' },
  { key: 'every2h', expr: '0 */2 * * *' },
  { key: 'every4h', expr: '0 */4 * * *' },
] as const

export function ScheduleManager({ workspaceId }: ScheduleManagerProps) {
  const { t } = useTranslation('workspaces')
  const queryClient = useQueryClient()
  const [showForm, setShowForm] = useState(false)
  const [scheduleType, setScheduleType] = useState<'cron' | 'once'>('cron')
  const [name, setName] = useState('')
  const [cronExpr, setCronExpr] = useState('')
  const [runAt, setRunAt] = useState('')
  const [defaultMessage, setDefaultMessage] = useState('')
  const [submitting, setSubmitting] = useState(false)

  const { data: schedules = [], refetch } = useQuery({
    queryKey: ['workspace-schedules', workspaceId],
    queryFn: () =>
      api.get<WorkspaceSchedule[]>(`/workspaces/${workspaceId}/schedules`),
  })

  const invalidate = () => {
    refetch()
    queryClient.invalidateQueries({
      queryKey: ['workspace-schedules', workspaceId],
    })
  }

  const handleAdd = async () => {
    if (scheduleType === 'cron' && !cronExpr.trim()) return
    if (scheduleType === 'once' && !runAt) return

    setSubmitting(true)
    try {
      await api.post(`/workspaces/${workspaceId}/schedules`, {
        name: name.trim() || undefined,
        schedule_type: scheduleType,
        cron_expr: scheduleType === 'cron' ? cronExpr.trim() : undefined,
        run_at:
          scheduleType === 'once'
            ? new Date(runAt).toISOString()
            : undefined,
        default_message: defaultMessage.trim() || undefined,
      })
      invalidate()
      setShowForm(false)
      setName('')
      setCronExpr('')
      setRunAt('')
      setDefaultMessage('')
    } finally {
      setSubmitting(false)
    }
  }

  const handleToggle = async (schedule: WorkspaceSchedule) => {
    await api.post(
      `/workspaces/${workspaceId}/schedules/${schedule.id}/toggle`,
      { enabled: !schedule.enabled },
    )
    invalidate()
  }

  const handleDelete = async (scheduleId: number) => {
    await api.delete(`/workspaces/${workspaceId}/schedules/${scheduleId}`)
    invalidate()
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-1">
        <label className="block text-sm font-medium text-foreground">
          <Clock className="inline h-3.5 w-3.5 mr-1 -mt-0.5" />
          {t('panels.scheduleManager.label')}
        </label>
        <button
          onClick={() => setShowForm(!showForm)}
          className="flex items-center gap-0.5 text-xs text-blue-500 hover:text-blue-700"
        >
          <Plus className="h-3 w-3" />
          {t('panels.scheduleManager.add')}
        </button>
      </div>
      <p className="text-xs text-muted-foreground mb-2">
        {t('panels.scheduleManager.desc')}
      </p>

      {/* Schedule list */}
      {schedules.length === 0 ? (
        <p className="text-xs text-muted-foreground italic">{t('panels.scheduleManager.empty')}</p>
      ) : (
        <div className="rounded-md border border-border divide-y divide-border">
          {schedules.map((s) => (
            <div
              key={s.id}
              className="px-3 py-2 flex items-center gap-2"
            >
              {s.schedule_type === 'cron' ? (
                <RefreshCw className="h-3.5 w-3.5 text-muted-foreground/70 flex-shrink-0" />
              ) : (
                <Pin className="h-3.5 w-3.5 text-muted-foreground/70 flex-shrink-0" />
              )}
              <div className="flex-1 min-w-0">
                <span className="text-sm text-foreground truncate block">
                  {s.name ||
                    (s.schedule_type === 'cron'
                      ? s.cron_expr
                      : s.run_at
                        ? new Date(s.run_at).toLocaleString()
                        : t('panels.scheduleManager.onceFallback'))}
                </span>
                {s.name && (
                  <span className="text-xs text-muted-foreground/70 font-mono truncate block">
                    {s.schedule_type === 'cron'
                      ? s.cron_expr
                      : s.run_at
                        ? new Date(s.run_at).toLocaleString()
                        : ''}
                  </span>
                )}
              </div>

              {/* Toggle switch */}
              <button
                onClick={() => handleToggle(s)}
                className={cn(
                  'relative inline-flex h-5 w-9 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors',
                  s.enabled
                    ? 'bg-info'
                    : 'bg-muted',
                )}
              >
                <span
                  className={cn(
                    'pointer-events-none inline-block h-4 w-4 rounded-full bg-background shadow transform transition-transform',
                    s.enabled ? 'translate-x-4' : 'translate-x-0',
                  )}
                />
              </button>

              {/* Delete */}
              <button
                onClick={() => handleDelete(s.id)}
                className="p-1 text-muted-foreground/70 hover:text-destructive flex-shrink-0"
              >
                <Trash2 className="h-3 w-3" />
              </button>
            </div>
          ))}
        </div>
      )}

      {/* Add form */}
      {showForm && (
        <div className="mt-2 rounded-md border border-info/30 bg-info/5 p-3 space-y-2">
          {/* Name */}
          <div>
            <label className="block text-xs font-medium text-muted-foreground mb-1">
              {t('panels.scheduleManager.nameLabel')}
            </label>
            <input
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t('panels.scheduleManager.namePlaceholder')}
              className="w-full rounded border border-border bg-background px-2 py-1 text-sm focus:outline-none focus:ring-1 focus:ring-info"
            />
          </div>

          {/* Type toggle */}
          <div className="flex gap-2">
            <button
              onClick={() => setScheduleType('cron')}
              className={cn(
                'rounded px-2.5 py-1 text-xs transition-colors',
                scheduleType === 'cron'
                  ? 'bg-info text-white'
                  : 'bg-muted text-muted-foreground hover:bg-accent',
              )}
            >
              <RefreshCw className="inline h-3 w-3 mr-1 -mt-0.5" />
              {t('panels.scheduleManager.recurring')}
            </button>
            <button
              onClick={() => setScheduleType('once')}
              className={cn(
                'rounded px-2.5 py-1 text-xs transition-colors',
                scheduleType === 'once'
                  ? 'bg-info text-white'
                  : 'bg-muted text-muted-foreground hover:bg-accent',
              )}
            >
              <Pin className="inline h-3 w-3 mr-1 -mt-0.5" />
              {t('panels.scheduleManager.once')}
            </button>
          </div>

          {/* Cron input + presets */}
          {scheduleType === 'cron' && (
            <div>
              <label className="block text-xs font-medium text-muted-foreground mb-1">
                {t('panels.scheduleManager.cronLabel')}
              </label>
              <input
                type="text"
                value={cronExpr}
                onChange={(e) => setCronExpr(e.target.value)}
                placeholder="0 9 * * *"
                className="w-full rounded border border-border bg-background px-2 py-1 text-sm font-mono focus:outline-none focus:ring-1 focus:ring-info mb-1.5"
              />
              <div className="flex flex-wrap gap-1">
                {CRON_PRESETS.map((preset) => (
                  <button
                    key={preset.expr}
                    onClick={() => setCronExpr(preset.expr)}
                    className={cn(
                      'rounded px-2 py-0.5 text-xs transition-colors',
                      cronExpr === preset.expr
                        ? 'bg-info/10 text-info border border-info/30'
                        : 'bg-muted text-muted-foreground hover:bg-accent',
                    )}
                  >
                    {t(`panels.scheduleManager.presets.${preset.key}`)}
                  </button>
                ))}
              </div>
            </div>
          )}

          {/* Once datetime picker */}
          {scheduleType === 'once' && (
            <div>
              <label className="block text-xs font-medium text-muted-foreground mb-1">
                {t('panels.scheduleManager.runAtLabel')}
              </label>
              <input
                type="datetime-local"
                value={runAt}
                onChange={(e) => setRunAt(e.target.value)}
                className="w-full rounded border border-border bg-background px-2 py-1 text-sm focus:outline-none focus:ring-1 focus:ring-info"
              />
            </div>
          )}

          {/* Default message */}
          <div>
            <label className="block text-xs font-medium text-muted-foreground mb-1">
              {t('panels.scheduleManager.defaultMessageLabel')}
            </label>
            <textarea
              value={defaultMessage}
              onChange={(e) => setDefaultMessage(e.target.value)}
              placeholder={t('panels.scheduleManager.defaultMessagePlaceholder')}
              rows={3}
              className="w-full rounded border border-input bg-background px-2 py-1 text-sm focus:outline-none focus:ring-1 focus:ring-blue-400 resize-none"
            />
            <p className="text-xs text-muted-foreground mt-0.5">{t('panels.scheduleManager.defaultMessageDesc')}</p>
          </div>

          {/* Actions */}
          <div className="flex justify-end gap-2 pt-1">
            <button
              onClick={() => {
                setShowForm(false)
                setName('')
                setDefaultMessage('')
                setCronExpr('')
                setRunAt('')
              }}
              className="rounded px-2.5 py-1 text-xs text-muted-foreground hover:bg-accent"
            >
              {t('common:actions.cancel')}
            </button>
            <button
              onClick={handleAdd}
              disabled={
                submitting ||
                (scheduleType === 'cron' && !cronExpr.trim()) ||
                (scheduleType === 'once' && !runAt)
              }
              className="rounded bg-info px-2.5 py-1 text-xs text-white hover:bg-info/90 disabled:opacity-50"
            >
              {submitting ? t('panels.scheduleManager.submitting') : t('panels.scheduleManager.submit')}
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
