import type { TFunction } from 'i18next'
import { useTranslation } from 'react-i18next'
import { Link } from '@tanstack/react-router'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { OpsBudgetBar } from './budget-bar'
import { OpsRiskBadge } from './risk-badge'
import { OpsStatusBadge } from './status-badge'
import type { OpsAccount } from '@/types/ops'

function accountName(account: OpsAccount) {
  return account.display_name || account.account_ref || `#${account.bot_id}`
}

function packLabel(t: TFunction, account: OpsAccount) {
  if (!account.pack_id) return t('packUnbound')
  return account.pack_version > 0
    ? `${account.pack_id} ${t('packVersion', { version: account.pack_version })}`
    : account.pack_id
}

function ToggleButton({
  account,
  busy,
  onToggleStatus,
}: {
  account: OpsAccount
  busy: boolean
  onToggleStatus: (account: OpsAccount) => void
}) {
  const { t } = useTranslation('ops')
  return (
    <Button
      size="sm"
      variant="outline"
      disabled={busy}
      onClick={() => onToggleStatus(account)}
      aria-label={
        account.status === 'active'
          ? t('actions.pause') + ' ' + accountName(account)
          : t('actions.resume') + ' ' + accountName(account)
      }
    >
      {account.status === 'active' ? t('actions.pause') : t('actions.resume')}
    </Button>
  )
}

function DetailsLink({ account }: { account: OpsAccount }) {
  const { t } = useTranslation('ops')
  return (
    <Button asChild size="sm" variant="ghost">
      <Link to="/ops/$botId" params={{ botId: String(account.bot_id) }}>
        {t('actions.details')}
      </Link>
    </Button>
  )
}

/**
 * Account rows for one channel. Desktop gets a table (the console is scanned by
 * column); mobile gets cards, because a 9-column table at phone width is only
 * usable by horizontal scrolling.
 */
export function AccountList({
  accounts,
  busyBotId,
  onToggleStatus,
}: {
  accounts: OpsAccount[]
  busyBotId?: number | null
  onToggleStatus: (account: OpsAccount) => void
}) {
  const { t } = useTranslation('ops')

  return (
    <>
      <div className="hidden md:block">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('columns.account')}</TableHead>
              <TableHead>{t('columns.status')}</TableHead>
              <TableHead>{t('columns.pack')}</TableHead>
              <TableHead>{t('columns.operator')}</TableHead>
              <TableHead className="text-right">{t('columns.output')}</TableHead>
              <TableHead>{t('columns.budget')}</TableHead>
              <TableHead className="text-right">{t('columns.approvals')}</TableHead>
              <TableHead>{t('columns.risk')}</TableHead>
              <TableHead className="text-right">{t('columns.actions')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {accounts.map((account) => (
              <TableRow key={account.bot_id} className="align-top">
                <TableCell>
                  <div className="flex flex-col gap-0.5">
                    <Link
                      to="/ops/$botId"
                      params={{ botId: String(account.bot_id) }}
                      className="font-medium text-foreground hover:text-brand hover:underline"
                    >
                      {accountName(account)}
                    </Link>
                    <span className="text-xs text-muted-foreground">{account.account_ref}</span>
                    {account.error ? (
                      <span className="text-xs text-destructive-text">{account.error}</span>
                    ) : null}
                  </div>
                </TableCell>
                <TableCell>
                  <OpsStatusBadge status={account.status} />
                </TableCell>
                <TableCell className="text-xs text-muted-foreground">
                  {packLabel(t, account)}
                </TableCell>
                <TableCell className="text-xs">{account.operator}</TableCell>
                <TableCell className="text-right text-xs tabular-nums">
                  {t('output', { today: account.output.today, week: account.output.week })}
                </TableCell>
                <TableCell>
                  <OpsBudgetBar budget={account.budget} />
                </TableCell>
                <TableCell className="text-right text-xs tabular-nums">
                  {account.pending_approvals > 0 ? (
                    <Link
                      to="/ops/$botId"
                      params={{ botId: String(account.bot_id) }}
                      className="text-brand hover:underline"
                    >
                      {t('approvalsCount', { count: account.pending_approvals })}
                    </Link>
                  ) : (
                    '—'
                  )}
                </TableCell>
                <TableCell>
                  <OpsRiskBadge level={account.risk} reasons={account.risk_reasons} />
                </TableCell>
                <TableCell>
                  <div className="flex justify-end gap-1">
                    <ToggleButton
                      account={account}
                      busy={busyBotId === account.bot_id}
                      onToggleStatus={onToggleStatus}
                    />
                    <DetailsLink account={account} />
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>

      <div className="space-y-2 md:hidden">
        {accounts.map((account) => (
          <div key={account.bot_id} className="rounded-lg border bg-warm-surface p-3 space-y-2">
            <div className="flex items-start justify-between gap-2">
              <div className="min-w-0">
                <Link
                  to="/ops/$botId"
                  params={{ botId: String(account.bot_id) }}
                  className="block truncate font-medium text-foreground hover:text-brand hover:underline"
                >
                  {accountName(account)}
                </Link>
                <span className="block truncate text-xs text-muted-foreground">
                  {account.account_ref}
                </span>
                {account.error ? (
                  <span className="block text-xs text-destructive-text">{account.error}</span>
                ) : null}
              </div>
              <OpsStatusBadge status={account.status} />
            </div>

            <dl className="grid grid-cols-2 gap-x-3 gap-y-1 text-xs">
              <dt className="text-muted-foreground">{t('columns.pack')}</dt>
              <dd className="truncate">{packLabel(t, account)}</dd>
              <dt className="text-muted-foreground">{t('columns.operator')}</dt>
              <dd className="truncate">{account.operator}</dd>
              <dt className="text-muted-foreground">{t('columns.output')}</dt>
              <dd className="tabular-nums">
                {t('output', { today: account.output.today, week: account.output.week })}
              </dd>
              <dt className="text-muted-foreground">{t('columns.approvals')}</dt>
              <dd className="tabular-nums">
                {account.pending_approvals > 0 ? (
                  <Link
                    to="/ops/$botId"
                    params={{ botId: String(account.bot_id) }}
                    className="text-brand hover:underline"
                  >
                    {t('approvalsCount', { count: account.pending_approvals })}
                  </Link>
                ) : (
                  '—'
                )}
              </dd>
            </dl>

            <OpsBudgetBar budget={account.budget} />

            <div className="flex flex-wrap items-center justify-between gap-2">
              <OpsRiskBadge level={account.risk} reasons={account.risk_reasons} />
              <div className="flex gap-1">
                <ToggleButton
                  account={account}
                  busy={busyBotId === account.bot_id}
                  onToggleStatus={onToggleStatus}
                />
                <DetailsLink account={account} />
              </div>
            </div>
          </div>
        ))}
      </div>
    </>
  )
}
