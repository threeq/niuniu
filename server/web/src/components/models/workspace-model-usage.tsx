import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { getBudget, getUsage, type OwnerParam } from '@/lib/model-api'
import type { ModelBudget } from '@/types/model'

const numberFormat = new Intl.NumberFormat()

interface WorkspaceModelUsageProps {
  /** Workspace id as a string or number; matched against the usage rows. */
  workspaceId: string | number
  /** The workspace's configured model (NIUNIU_MODEL), if any. */
  model?: string
  /** Provider the running agent process resolved at spawn time, if any. */
  provider?: string
  /** Owner scope of the workspace's project; omitted = personal owner. */
  owner?: OwnerParam
}

function BudgetStatus({ budget }: { budget: ModelBudget | undefined }) {
  const { t } = useTranslation('settings')
  if (!budget) {
    return (
      <span className="text-sm text-warm-text-muted">
        {t('models.workspace.loading')}
      </span>
    )
  }
  if (budget.monthly_tokens <= 0) {
    return (
      <span className="inline-flex items-center rounded border border-warm-border bg-warm-muted px-1.5 py-0.5 text-xs text-warm-text-muted">
        {t('models.budget.status.unlimited')}
      </span>
    )
  }
  if (budget.used_tokens >= budget.monthly_tokens) {
    return (
      <span className="inline-flex items-center rounded border border-destructive/30 bg-destructive/10 px-1.5 py-0.5 text-xs text-destructive-text">
        {t('models.budget.status.exceeded')}
      </span>
    )
  }
  const percent = Math.round((budget.used_tokens / budget.monthly_tokens) * 100)
  if (percent >= 80) {
    return (
      <span className="inline-flex items-center rounded border border-warning/30 bg-warning/15 px-1.5 py-0.5 text-xs text-warning">
        {t('models.budget.status.near', { percent })}
      </span>
    )
  }
  return (
    <span className="inline-flex items-center rounded border border-success/30 bg-success/15 px-1.5 py-0.5 text-xs text-success">
      {t('models.budget.status.ok', { percent })}
    </span>
  )
}

/**
 * Workspace settings summary: which model this workspace runs, how many tokens
 * it has used this month, and where its owner stands against the monthly cap.
 */
export function WorkspaceModelUsage({
  workspaceId,
  model,
  provider,
  owner,
}: WorkspaceModelUsageProps) {
  const { t } = useTranslation('settings')
  const key = String(workspaceId)

  const budgetQuery = useQuery({
    queryKey: ['model-budget', owner?.type ?? 'me', owner?.id ?? 0],
    queryFn: () => getBudget(owner),
  })
  const usageQuery = useQuery({
    queryKey: ['model-usage', 'workspace', key, owner?.type ?? 'me', owner?.id ?? 0],
    queryFn: () => getUsage('workspace', owner),
  })

  const tokenRow = usageQuery.data?.by_workspace?.find((row) => row.key === key)

  return (
    <div className="rounded-md border border-border p-3 space-y-2">
      <p className="text-sm font-medium text-foreground">
        {t('models.workspace.title')}
      </p>
      <div className="flex items-center justify-between gap-4 text-sm">
        <span className="text-muted-foreground">
          {t('models.workspace.currentProvider')}
        </span>
        <span className="font-mono text-foreground">
          {provider?.trim() || '—'}
        </span>
      </div>
      <div className="flex items-center justify-between gap-4 text-sm">
        <span className="text-muted-foreground">
          {t('models.workspace.currentModel')}
        </span>
        <span className="font-mono text-foreground">
          {model?.trim() || t('models.workspace.defaultModel')}
        </span>
      </div>
      <div className="flex items-center justify-between gap-4 text-sm">
        <span className="text-muted-foreground">
          {t('models.workspace.monthTokens')}
        </span>
        <span className="text-foreground">
          {usageQuery.isLoading
            ? t('models.workspace.loading')
            : numberFormat.format(tokenRow?.total_tokens ?? 0)}
        </span>
      </div>
      <div className="flex items-center justify-between gap-4 text-sm">
        <span className="text-muted-foreground">
          {t('models.workspace.budgetStatus')}
        </span>
        <BudgetStatus budget={budgetQuery.data} />
      </div>
    </div>
  )
}
