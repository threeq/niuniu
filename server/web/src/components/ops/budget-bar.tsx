import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import type { OpsBudgetSnapshot } from '@/types/ops'

const money = (value: number) =>
  new Intl.NumberFormat(undefined, { maximumFractionDigits: 2 }).format(value)

export function OpsBudgetBar({
  budget,
  className,
}: {
  budget: OpsBudgetSnapshot
  className?: string
}) {
  const { t } = useTranslation('ops')
  if (!budget.configured) {
    return (
      <span className={cn('text-xs text-muted-foreground', className)}>
        {t('budget.notConfigured')}
      </span>
    )
  }

  const spent = Math.max(0, budget.spent)
  const ratio = budget.limit > 0 ? spent / budget.limit : 0
  const pct = Math.min(100, Math.max(0, ratio * 100))
  const over = spent > budget.limit
  const low = !over && ratio >= 0.9

  return (
    <div className={cn('min-w-28 space-y-1', className)}>
      <div className={cn('text-xs tabular-nums', over ? 'text-destructive-text' : 'text-foreground')}>
        {t('budget.remainingOf', {
          amount: money(budget.remaining),
          limit: money(budget.limit),
          currency: budget.currency,
        })}
      </div>
      <div
        className="h-2 w-full overflow-hidden rounded-full bg-warm-muted"
        role="progressbar"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={Math.round(pct)}
      >
        <div
          className={cn(
            'h-full rounded-full transition-colors',
            over ? 'bg-destructive' : low ? 'bg-warning' : 'bg-success',
          )}
          style={{ width: `${pct}%` }}
        />
      </div>
    </div>
  )
}
