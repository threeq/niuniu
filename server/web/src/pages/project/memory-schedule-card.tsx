import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { memoryApi } from '@/lib/memory-api'

// Per-project automatic memory-maintenance schedule, stored as a 5-field cron
// expression (empty = OFF, the default). The UI is an enable toggle plus a
// friendly builder (frequency + time + weekday/day) that compiles to cron, an
// advanced raw-cron mode, and a manual "run once" trigger.

const DEFAULT_CRON = '33 3 3 * *'

type Freq = 'daily' | 'weekly' | 'monthly'
interface Simple {
  freq: Freq
  hour: number
  minute: number
  weekday: number // cron dow 0..6 (0 = Sunday)
  dom: number // day of month 1..28
}

const pad = (n: number) => String(n).padStart(2, '0')

function parseCron(expr: string): Simple | null {
  const f = expr.trim().split(/\s+/)
  if (f.length !== 5) return null
  const [m, h, dom, mon, dow] = f
  const mi = Number(m)
  const hi = Number(h)
  if (mon !== '*') return null
  if (!Number.isInteger(mi) || mi < 0 || mi > 59) return null
  if (!Number.isInteger(hi) || hi < 0 || hi > 23) return null
  if (dom === '*' && dow === '*') return { freq: 'daily', hour: hi, minute: mi, weekday: 1, dom: 1 }
  if (dom === '*' && /^[0-6]$/.test(dow)) return { freq: 'weekly', hour: hi, minute: mi, weekday: Number(dow), dom: 1 }
  if (dow === '*' && /^([1-9]|1[0-9]|2[0-8])$/.test(dom)) return { freq: 'monthly', hour: hi, minute: mi, weekday: 1, dom: Number(dom) }
  return null
}

function buildCron(s: Simple): string {
  if (s.freq === 'daily') return `${s.minute} ${s.hour} * * *`
  if (s.freq === 'weekly') return `${s.minute} ${s.hour} * * ${s.weekday}`
  return `${s.minute} ${s.hour} ${s.dom} * *`
}

interface Props {
  projectId: number
}

export function ProjectMemoryScheduleCard({ projectId }: Props) {
  const { t } = useTranslation('projects')
  const qc = useQueryClient()
  const [enabled, setEnabled] = useState(false)
  const [simple, setSimple] = useState<Simple | null>(null)
  const [custom, setCustom] = useState('')
  const [advanced, setAdvanced] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [savedTick, setSavedTick] = useState(false)

  const { data } = useQuery({
    queryKey: ['memory-schedule', projectId],
    queryFn: () => memoryApi.getSchedule(projectId),
  })

  const [seeded, setSeeded] = useState(false)
  if (data && !seeded) {
    setSeeded(true)
    const cron = (data.cron || '').trim()
    setEnabled(cron !== '')
    setCustom(cron || DEFAULT_CRON)
    const parsed = parseCron(cron || DEFAULT_CRON)
    if (parsed) {
      setSimple(parsed)
    } else {
      setAdvanced(true)
      setSimple({ freq: 'monthly', hour: 3, minute: 33, weekday: 1, dom: 3 })
    }
  }

  const builtCron = advanced ? custom.trim() : simple ? buildCron(simple) : ''
  // What we persist: empty string when the feature is toggled off.
  const effectiveCron = enabled ? builtCron : ''

  const mutation = useMutation({
    mutationFn: async (cron: string) => memoryApi.setSchedule(projectId, cron),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['memory-schedule', projectId] })
      setSavedTick(true)
      setTimeout(() => setSavedTick(false), 2000)
    },
    onError: (e: unknown) =>
      setError(t('tabs.settings.memory.saveFailed', { message: e instanceof Error ? e.message : String(e) })),
  })

  if (!simple) {
    return <div className="text-sm text-muted-foreground">…</div>
  }

  const selectClass =
    'h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-ring focus:border-transparent'
  const weekdays = [0, 1, 2, 3, 4, 5, 6]

  return (
    <div className="border rounded-lg p-4 space-y-4">
      <div>
        <h3 className="text-sm font-semibold text-foreground">{t('tabs.settings.memory.title')}</h3>
        <p className="text-xs text-muted-foreground mt-1">{t('tabs.settings.memory.description')}</p>
      </div>

      <label className="flex items-start gap-2 cursor-pointer select-none">
        <input
          type="checkbox"
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
          className="mt-0.5 h-4 w-4 rounded border-border accent-primary"
        />
        <span>
          <span className="text-sm font-medium text-foreground">{t('tabs.settings.memory.enable')}</span>
          <span className="block text-xs text-muted-foreground/70">{t('tabs.settings.memory.enableHint')}</span>
        </span>
      </label>

      {enabled && (
      <div className="space-y-3">
        <label className="text-sm font-medium text-foreground">{t('tabs.settings.memory.schedule')}</label>

        {!advanced && (
          <div className="flex flex-wrap items-center gap-2">
            <select
              value={simple.freq}
              onChange={(e) => setSimple({ ...simple, freq: e.target.value as Freq })}
              className={selectClass}
            >
              <option value="daily">{t('tabs.settings.memory.freqDaily')}</option>
              <option value="weekly">{t('tabs.settings.memory.freqWeekly')}</option>
              <option value="monthly">{t('tabs.settings.memory.freqMonthly')}</option>
            </select>

            {simple.freq === 'weekly' && (
              <select
                value={simple.weekday}
                onChange={(e) => setSimple({ ...simple, weekday: Number(e.target.value) })}
                className={selectClass}
              >
                {weekdays.map((d) => (
                  <option key={d} value={d}>{t(`tabs.settings.memory.weekdays.${d}`)}</option>
                ))}
              </select>
            )}

            {simple.freq === 'monthly' && (
              <select
                value={simple.dom}
                onChange={(e) => setSimple({ ...simple, dom: Number(e.target.value) })}
                className={selectClass}
              >
                {Array.from({ length: 28 }, (_, i) => i + 1).map((d) => (
                  <option key={d} value={d}>{t('tabs.settings.memory.domOption', { day: d })}</option>
                ))}
              </select>
            )}

            <span className="text-sm text-muted-foreground">{t('tabs.settings.memory.atTime')}</span>
            <input
              type="time"
              value={`${pad(simple.hour)}:${pad(simple.minute)}`}
              onChange={(e) => {
                const [h, m] = e.target.value.split(':').map(Number)
                if (Number.isInteger(h) && Number.isInteger(m)) setSimple({ ...simple, hour: h, minute: m })
              }}
              className={selectClass}
            />
          </div>
        )}

        {advanced && (
          <div className="space-y-2">
            <input
              type="text"
              value={custom}
              onChange={(e) => setCustom(e.target.value)}
              placeholder="33 3 3 * *"
              className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm font-mono text-foreground focus:outline-none focus:ring-2 focus:ring-ring focus:border-transparent"
            />
            <p className="text-xs text-muted-foreground/70">{t('tabs.settings.memory.customHint')}</p>
          </div>
        )}

        <div className="flex items-center justify-between">
          <p className="text-xs text-muted-foreground/70">{t('tabs.settings.memory.cronPreview', { cron: effectiveCron })}</p>
          <button
            type="button"
            onClick={() => setAdvanced((a) => !a)}
            className="text-xs text-info hover:underline"
          >
            {advanced ? t('tabs.settings.memory.useSimple') : t('tabs.settings.memory.useAdvanced')}
          </button>
        </div>
      </div>
      )}

      {error && <p className="text-sm text-destructive">{error}</p>}
      <div className="flex flex-wrap items-center gap-3">
        <Button
          size="sm"
          disabled={(enabled && !builtCron) || mutation.isPending}
          onClick={() => {
            setError(null)
            mutation.mutate(effectiveCron)
          }}
        >
          {mutation.isPending ? t('common:actions.saving') : t('tabs.settings.memory.save')}
        </Button>
        {savedTick && <span className="text-sm text-success">{t('tabs.settings.memory.saved')}</span>}
      </div>
    </div>
  )
}
