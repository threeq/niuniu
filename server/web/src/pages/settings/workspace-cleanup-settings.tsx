import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { adminSettingsApi } from '@/lib/api'

// Server-side key for the global workspace expiry window (days). 0 = disabled.
const KEY = 'workspace.expire_days'

/**
 * Global workspace expiry. The server's hourly sweeper deletes every workspace
 * whose last activity is older than this many days, together with its linked
 * issue. Kept separate from the per-project cleanup policy shown on the project
 * page: that one only targets completed / not-started issues.
 */
export function WorkspaceCleanupSettings() {
  const { t } = useTranslation('settings')
  const qc = useQueryClient()
  const [value, setValue] = useState('')
  const [seeded, setSeeded] = useState(false)
  const [savedTick, setSavedTick] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const { data } = useQuery({
    queryKey: ['admin-setting', KEY],
    queryFn: () => adminSettingsApi.get(KEY),
  })
  if (data && !seeded) {
    setSeeded(true)
    setValue(data.value)
  }

  const mutation = useMutation({
    mutationFn: (v: string) => adminSettingsApi.put(KEY, v),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['admin-setting', KEY] })
      setError(null)
      setSavedTick(true)
      setTimeout(() => setSavedTick(false), 2000)
    },
    onError: (e: unknown) => setError(e instanceof Error ? e.message : String(e)),
  })

  const days = Number(value)
  const valid = value.trim() !== '' && Number.isInteger(days) && days >= 0
  const enabled = valid && days > 0

  return (
    <div className="py-6 space-y-8 max-w-xl">
      <div>
        <h2 className="text-lg font-medium text-foreground">{t('workspaceCleanup.title')}</h2>
        <p className="text-sm text-muted-foreground mt-1">{t('workspaceCleanup.description')}</p>
      </div>

      <div className="space-y-2">
        <label className="text-sm font-medium text-foreground" htmlFor="workspace-expire-days">
          {t('workspaceCleanup.expireDays')}
        </label>
        <div className="flex items-center gap-3">
          <input
            id="workspace-expire-days"
            type="number"
            min={0}
            value={value}
            onChange={(e) => {
              setValue(e.target.value)
              setSavedTick(false)
            }}
            className="w-40 h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-ring focus:border-transparent"
          />
          <span className={enabled ? 'text-xs text-muted-foreground' : 'text-xs text-muted-foreground/70'}>
            {enabled
              ? t('workspaceCleanup.enabledHint', { days })
              : t('workspaceCleanup.disabledHint')}
          </span>
        </div>
        <p className="text-xs text-muted-foreground/70">{t('workspaceCleanup.expireDaysHint')}</p>
      </div>

      {error && <p className="text-sm text-destructive">{t('workspaceCleanup.saveFailed', { message: error })}</p>}

      <div className="flex items-center gap-3">
        <Button
          size="sm"
          disabled={!valid || mutation.isPending}
          onClick={() => {
            setError(null)
            mutation.mutate(value.trim())
          }}
        >
          {t('workspaceCleanup.save')}
        </Button>
        {savedTick && <span className="text-sm text-muted-foreground">{t('workspaceCleanup.saved')}</span>}
      </div>
    </div>
  )
}
