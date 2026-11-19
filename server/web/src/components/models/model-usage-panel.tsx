import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { getUsage } from '@/lib/model-api'
import type {
  ModelUsageGroup,
  ModelUsageRow,
  ModelUsageSummary,
} from '@/types/model'
import { cn } from '@/lib/utils'

const GROUPS: { id: ModelUsageGroup; labelKey: string }[] = [
  { id: 'model', labelKey: 'models.usage.groups.model' },
  { id: 'cli', labelKey: 'models.usage.groups.cli' },
  { id: 'provider', labelKey: 'models.usage.groups.provider' },
  { id: 'project', labelKey: 'models.usage.groups.project' },
]

const ROWS_KEY: Record<ModelUsageGroup, keyof ModelUsageSummary> = {
  model: 'by_model',
  cli: 'by_cli',
  provider: 'by_provider',
  project: 'by_project',
  workspace: 'by_workspace',
}

const numberFormat = new Intl.NumberFormat()

function formatTokens(value: number): string {
  return numberFormat.format(value)
}

/**
 * Settings → 模型 → 用量: the owner's current-month total plus a breakdown
 * along one dimension at a time. The backend returns a single breakdown per
 * request, so the dimension selector drives its own query.
 */
export function ModelUsagePanel() {
  const { t } = useTranslation('settings')
  const [group, setGroup] = useState<ModelUsageGroup>('model')

  const totalQuery = useQuery({
    queryKey: ['model-usage', 'summary', ''],
    queryFn: () => getUsage(),
  })
  const groupQuery = useQuery({
    queryKey: ['model-usage', 'summary', group],
    queryFn: () => getUsage(group),
  })

  const rows: ModelUsageRow[] =
    (groupQuery.data?.[ROWS_KEY[group]] as ModelUsageRow[] | undefined) ?? []

  return (
    <div className="space-y-4">
      <Card>
        <CardContent className="p-4">
          {totalQuery.isLoading ? (
            <p className="text-sm text-warm-text-muted">
              {t('models.usage.loading')}
            </p>
          ) : totalQuery.isError || !totalQuery.data ? (
            <p className="text-sm text-destructive-text">
              {t('models.usage.loadFailed')}
            </p>
          ) : (
            <div className="flex flex-wrap gap-8">
              <div>
                <p className="text-xs text-warm-text-muted">
                  {t('models.usage.totalTokens')}
                </p>
                <p className="mt-1 text-2xl font-semibold text-warm-text">
                  {formatTokens(totalQuery.data.total_tokens)}
                </p>
              </div>
              <div>
                <p className="text-xs text-warm-text-muted">
                  {t('models.usage.totalRequests')}
                </p>
                <p className="mt-1 text-2xl font-semibold text-warm-text">
                  {formatTokens(totalQuery.data.requests)}
                </p>
              </div>
            </div>
          )}
        </CardContent>
      </Card>

      <div
        className="inline-flex rounded-md bg-warm-muted p-1"
        role="tablist"
        aria-label={t('models.usage.title')}
      >
        {GROUPS.map((item) => (
          <Button
            key={item.id}
            type="button"
            role="tab"
            size="sm"
            variant="ghost"
            aria-selected={group === item.id}
            onClick={() => setGroup(item.id)}
            className={cn(
              'text-warm-text-muted hover:bg-transparent hover:text-warm-text',
              group === item.id && 'bg-warm-surface text-warm-text shadow-sm'
            )}
          >
            {t(item.labelKey)}
          </Button>
        ))}
      </div>

      <div className="rounded-md border border-warm-border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('models.usage.columns.name')}</TableHead>
              <TableHead className="text-right">
                {t('models.usage.columns.tokens')}
              </TableHead>
              <TableHead className="text-right">
                {t('models.usage.columns.requests')}
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {groupQuery.isLoading ? (
              <TableRow>
                <TableCell colSpan={3} className="text-sm text-warm-text-muted">
                  {t('models.usage.loading')}
                </TableCell>
              </TableRow>
            ) : groupQuery.isError ? (
              <TableRow>
                <TableCell
                  colSpan={3}
                  className="text-sm text-destructive-text"
                >
                  {t('models.usage.loadFailed')}
                </TableCell>
              </TableRow>
            ) : rows.length === 0 ? (
              <TableRow>
                <TableCell colSpan={3} className="text-sm text-warm-text-muted">
                  {t('models.usage.empty')}
                </TableCell>
              </TableRow>
            ) : (
              rows.map((row) => (
                <TableRow key={row.key}>
                  <TableCell className="font-mono text-sm">{row.key}</TableCell>
                  <TableCell className="text-right text-sm">
                    {formatTokens(row.total_tokens)}
                  </TableCell>
                  <TableCell className="text-right text-sm">
                    {formatTokens(row.requests)}
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </div>
    </div>
  )
}
