import { useState } from 'react'
import { useQueries, useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { adminSettingsApi } from '@/lib/api'

const KEYS = {
  budget: 'orchestration.chain_cost_budget_usd',
  maxConcurrent: 'orchestration.max_concurrent_workspaces',
  maxBatch: 'orchestration.max_batch_issues',
  warnPct: 'orchestration.chain_cost_warn_ratio',
} as const

type FormState = { budget: number; maxConcurrent: number; maxBatch: number; warnPct: number }

export function OrchestrationSettings() {
  const { t } = useTranslation('settings')
  const qc = useQueryClient()
  const [form, setForm] = useState<FormState | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [savedTick, setSavedTick] = useState(false)

  const results = useQueries({
    queries: Object.values(KEYS).map((key) => ({
      queryKey: ['admin-setting', key],
      queryFn: () => adminSettingsApi.get(key),
    })),
  })
  const allLoaded = results.every((r) => r.data)

  // Seed local form once the four values arrive.
  const [seeded, setSeeded] = useState(false)
  if (allLoaded && !seeded) {
    setSeeded(true)
    setForm({
      budget: Number(results[0].data!.value),
      maxConcurrent: Number(results[1].data!.value),
      maxBatch: Number(results[2].data!.value),
      warnPct: Number(results[3].data!.value),
    })
  }

  const mutation = useMutation({
    mutationFn: async (f: FormState) => {
      await adminSettingsApi.put(KEYS.budget, String(f.budget))
      await adminSettingsApi.put(KEYS.maxConcurrent, String(f.maxConcurrent))
      await adminSettingsApi.put(KEYS.maxBatch, String(f.maxBatch))
      await adminSettingsApi.put(KEYS.warnPct, String(f.warnPct))
    },
    onSuccess: () => {
      Object.values(KEYS).forEach((key) => qc.invalidateQueries({ queryKey: ['admin-setting', key] }))
      setSavedTick(true)
      setTimeout(() => setSavedTick(false), 2000)
    },
    onError: (e: unknown) => setError(e instanceof Error ? e.message : String(e)),
  })

  if (!form) {
    return <div className="py-6 text-sm text-muted-foreground">…</div>
  }

  const valid =
    Number.isInteger(form.budget) && form.budget >= 0 &&
    Number.isInteger(form.maxConcurrent) && form.maxConcurrent >= 0 &&
    Number.isInteger(form.maxBatch) && form.maxBatch >= 0 &&
    Number.isInteger(form.warnPct) && form.warnPct >= 0 && form.warnPct <= 100

  const numberField = (
    field: 'budget' | 'maxConcurrent' | 'maxBatch',
    labelKey: string,
    hintKey: string,
  ) => (
    <div className="space-y-2">
      <label className="text-sm font-medium text-foreground">{t(labelKey)}</label>
      <input
        type="number"
        min={0}
        value={form[field]}
        onChange={(e) => setForm({ ...form, [field]: Math.trunc(Number(e.target.value)) })}
        className="w-40 h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-ring focus:border-transparent"
      />
      <p className="text-xs text-muted-foreground/70">{t(hintKey)}</p>
    </div>
  )

  return (
    <div className="py-6 space-y-8 max-w-xl">
      <div>
        <h2 className="text-lg font-medium text-foreground">{t('orchestration.title')}</h2>
        <p className="text-sm text-muted-foreground mt-1">{t('orchestration.description')}</p>
      </div>
      {numberField('budget', 'orchestration.budget', 'orchestration.budgetHint')}
      {numberField('maxConcurrent', 'orchestration.maxConcurrent', 'orchestration.maxConcurrentHint')}
      {numberField('maxBatch', 'orchestration.maxBatch', 'orchestration.maxBatchHint')}
      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <label className="text-sm font-medium text-foreground">{t('orchestration.warnRatio')}</label>
          <span className="text-sm text-muted-foreground tabular-nums">{form.warnPct}%</span>
        </div>
        <input
          type="range"
          min={0}
          max={100}
          step={5}
          value={form.warnPct}
          onChange={(e) => setForm({ ...form, warnPct: Number(e.target.value) })}
          className="w-full h-1.5 bg-muted rounded-full appearance-none cursor-pointer accent-info [&::-webkit-slider-thumb]:appearance-none [&::-webkit-slider-thumb]:h-4 [&::-webkit-slider-thumb]:w-4 [&::-webkit-slider-thumb]:rounded-full [&::-webkit-slider-thumb]:bg-info"
        />
        <p className="text-xs text-muted-foreground/70">{t('orchestration.warnRatioHint')}</p>
      </div>
      {error && <p className="text-sm text-destructive">{t('orchestration.saveFailed', { message: error })}</p>}
      <div className="flex items-center gap-3">
        <Button
          size="sm"
          disabled={!valid || mutation.isPending}
          onClick={() => {
            setError(null)
            mutation.mutate(form)
          }}
        >
          {mutation.isPending ? t('common:actions.saving') : t('orchestration.saveAll')}
        </Button>
        {savedTick && <span className="text-sm text-success">{t('orchestration.saved')}</span>}
      </div>
    </div>
  )
}
