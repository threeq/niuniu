import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { ShieldCheck } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { projectFloorApi } from '@/lib/project-floor-api'

// Per-project 底线 (floor) command. One shell command that must exit 0 before an
// issue may complete. Empty = no floor. This is the single-field replacement for
// configuring the engineering-standards rule library by hand.

const DEFAULT_TIMEOUT_SEC = 600
const MAX_TIMEOUT_SEC = 600

interface Props {
  projectId: number
}

export function ProjectFloorCard({ projectId }: Props) {
  const { t } = useTranslation('projects')
  const qc = useQueryClient()
  const [command, setCommand] = useState('')
  const [timeoutSec, setTimeoutSec] = useState(DEFAULT_TIMEOUT_SEC)
  const [seeded, setSeeded] = useState(false)
  const [savedTick, setSavedTick] = useState(false)

  const { data } = useQuery({
    queryKey: ['project-floor', projectId],
    queryFn: () => projectFloorApi.get(projectId),
  })

  if (data && !seeded) {
    setSeeded(true)
    setCommand(data.command)
    setTimeoutSec(data.timeout_sec > 0 ? data.timeout_sec : DEFAULT_TIMEOUT_SEC)
  }

  const save = useMutation({
    mutationFn: () =>
      projectFloorApi.set(projectId, { command, timeout_sec: timeoutSec }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['project-floor', projectId] })
      setSavedTick(true)
      setTimeout(() => setSavedTick(false), 2000)
    },
    onError: (e: unknown) =>
      toast.error(
        t('tabs.settings.floor.saveFailed', {
          message: e instanceof Error ? e.message : String(e),
        }),
      ),
  })

  const invalid = timeoutSec <= 0 || timeoutSec > MAX_TIMEOUT_SEC

  return (
    <div className="border rounded-lg p-4 space-y-4">
      <div className="flex items-center gap-2">
        <ShieldCheck className="w-4 h-4 text-muted-foreground shrink-0" aria-hidden="true" />
        <h3 className="text-sm font-semibold text-foreground">{t('tabs.settings.floor.title')}</h3>
      </div>
      <p className="text-xs text-muted-foreground">{t('tabs.settings.floor.description')}</p>

      <div className="space-y-1.5">
        <label className="text-sm font-medium text-foreground" htmlFor="floor-command">
          {t('tabs.settings.floor.command')}
        </label>
        <Input
          id="floor-command"
          value={command}
          placeholder={t('tabs.settings.floor.commandPlaceholder')}
          onChange={(e) => setCommand(e.target.value)}
        />
        <p className="text-xs text-muted-foreground/70">{t('tabs.settings.floor.commandHint')}</p>
      </div>

      {command.trim() !== '' && (
        <div className="space-y-1.5">
          <label className="text-sm font-medium text-foreground" htmlFor="floor-timeout">
            {t('tabs.settings.floor.timeout')}
          </label>
          <div className="flex items-center gap-2">
            <Input
              id="floor-timeout"
              type="number"
              min={1}
              max={MAX_TIMEOUT_SEC}
              value={timeoutSec}
              onChange={(e) => setTimeoutSec(Number(e.target.value))}
              className="w-24"
            />
            <span className="text-sm text-muted-foreground">{t('tabs.settings.floor.secondsUnit')}</span>
          </div>
        </div>
      )}

      <div className="flex flex-wrap items-center gap-3">
        <Button size="sm" disabled={invalid || save.isPending} onClick={() => save.mutate()}>
          {save.isPending ? t('tabs.settings.floor.saving') : t('tabs.settings.floor.save')}
        </Button>
        {savedTick && <span className="text-sm text-success">{t('tabs.settings.floor.saved')}</span>}
      </div>
    </div>
  )
}
