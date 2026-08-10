import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { cleanupApi } from '@/lib/cleanup-api'
import type { CleanupStatus } from '@/types/api'

// Per-project workspace auto-cleanup policy. An hourly sweeper deletes each
// workspace (and its linked issue) whose issue is已完成 / 未开始 and that has had
// no activity for at least `inactiveDays` days. OFF by default.

const ALL_STATUSES: CleanupStatus[] = ['completed', 'not_started']

interface Props {
  projectId: number
}

export function ProjectCleanupPolicyCard({ projectId }: Props) {
  const { t } = useTranslation('projects')
  const qc = useQueryClient()
  const [enabled, setEnabled] = useState(false)
  const [days, setDays] = useState(30)
  const [statuses, setStatuses] = useState<CleanupStatus[]>(ALL_STATUSES)
  const [seeded, setSeeded] = useState(false)
  const [savedTick, setSavedTick] = useState(false)

  const { data } = useQuery({
    queryKey: ['cleanup-policy', projectId],
    queryFn: () => cleanupApi.getPolicy(projectId),
  })

  if (data && !seeded) {
    setSeeded(true)
    setEnabled(data.enabled)
    setDays(data.inactive_days > 0 ? data.inactive_days : 30)
    setStatuses(data.statuses.length > 0 ? data.statuses : ALL_STATUSES)
  }

  const save = useMutation({
    mutationFn: () =>
      cleanupApi.setPolicy(projectId, { enabled, inactive_days: days, statuses }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['cleanup-policy', projectId] })
      setSavedTick(true)
      setTimeout(() => setSavedTick(false), 2000)
    },
    onError: (e: unknown) =>
      toast.error(t('tabs.settings.cleanup.saveFailed', { message: e instanceof Error ? e.message : String(e) })),
  })

  const runNow = useMutation({
    mutationFn: () => cleanupApi.runOnce(projectId),
    onSuccess: (res) => {
      toast.success(t('tabs.settings.cleanup.ranSummary', { scanned: res.scanned, deleted: res.deleted.length }))
      qc.invalidateQueries({ queryKey: ['workspaces'] })
    },
    onError: (e: unknown) =>
      toast.error(t('tabs.settings.cleanup.runFailed', { message: e instanceof Error ? e.message : String(e) })),
  })

  const toggleStatus = (s: CleanupStatus) => {
    setStatuses((cur) => (cur.includes(s) ? cur.filter((x) => x !== s) : [...cur, s]))
  }

  const invalid = enabled && (days <= 0 || statuses.length === 0)

  return (
    <div className="border rounded-lg p-4 space-y-4">
      <div className="flex items-center gap-2">
        <Trash2 className="w-4 h-4 text-muted-foreground shrink-0" aria-hidden="true" />
        <h3 className="text-sm font-semibold text-foreground">{t('tabs.settings.cleanup.title')}</h3>
      </div>
      <p className="text-xs text-muted-foreground">{t('tabs.settings.cleanup.description')}</p>

      <label className="flex items-start gap-2 cursor-pointer select-none">
        <input
          type="checkbox"
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
          className="mt-0.5 h-4 w-4 rounded border-border accent-primary"
        />
        <span>
          <span className="text-sm font-medium text-foreground">{t('tabs.settings.cleanup.enable')}</span>
          <span className="block text-xs text-muted-foreground/70">{t('tabs.settings.cleanup.enableHint')}</span>
        </span>
      </label>

      {enabled && (
        <div className="space-y-4">
          {/* Inactive days */}
          <div className="space-y-1.5">
            <label className="text-sm font-medium text-foreground">{t('tabs.settings.cleanup.inactiveDays')}</label>
            <div className="flex items-center gap-2">
              <Input
                type="number"
                min={1}
                value={days}
                onChange={(e) => setDays(Number(e.target.value))}
                className="w-24"
              />
              <span className="text-sm text-muted-foreground">{t('tabs.settings.cleanup.daysUnit')}</span>
            </div>
          </div>

          {/* Target statuses */}
          <div className="space-y-1.5">
            <label className="text-sm font-medium text-foreground">{t('tabs.settings.cleanup.statuses')}</label>
            <div className="flex flex-wrap gap-3">
              {ALL_STATUSES.map((s) => (
                <label key={s} className="flex items-center gap-1.5 cursor-pointer select-none">
                  <input
                    type="checkbox"
                    checked={statuses.includes(s)}
                    onChange={() => toggleStatus(s)}
                    className="h-4 w-4 rounded border-border accent-primary"
                  />
                  <span className="text-sm text-foreground">{t(`tabs.settings.cleanup.status.${s}`)}</span>
                </label>
              ))}
            </div>
          </div>
        </div>
      )}

      <div className="flex flex-wrap items-center gap-3">
        <Button size="sm" disabled={invalid || save.isPending} onClick={() => save.mutate()}>
          {save.isPending ? t('tabs.settings.cleanup.saving') : t('tabs.settings.cleanup.save')}
        </Button>
        <Button
          size="sm"
          variant="outline"
          disabled={runNow.isPending}
          onClick={() => runNow.mutate()}
        >
          {runNow.isPending ? t('tabs.settings.cleanup.running') : t('tabs.settings.cleanup.runNow')}
        </Button>
        {savedTick && <span className="text-sm text-success">{t('tabs.settings.cleanup.saved')}</span>}
      </div>
    </div>
  )
}
