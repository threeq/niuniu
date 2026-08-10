import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronRight, ChevronDown, LogIn, Pencil, Eye, Trash2, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { AccountUsageCard } from './account-usage-card'
import type { ClaudeAccount } from '@/types/api'

interface AccountRowProps {
  account: ClaudeAccount
  isAdmin: boolean
  callerID: number
  personalMode: boolean
  onLogin: (account: ClaudeAccount) => void
  onRename: (account: ClaudeAccount) => void
  onToggleVisibility: (account: ClaudeAccount) => void
  onDelete: (account: ClaudeAccount) => void
  onRefresh: (account: ClaudeAccount) => void
}

function StatusBadge({ status }: { status: ClaudeAccount['status'] }) {
  const { t } = useTranslation('claude-accounts')
  const classes: Record<ClaudeAccount['status'], string> = {
    active: 'bg-success/15 text-success',
    pending: 'bg-warning/15 text-warning',
    failed: 'bg-destructive/15 text-destructive',
  }
  return (
    <span className={`inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-medium ${classes[status]}`}>
      {t(`status.${status}`)}
    </span>
  )
}

export function AccountRow({
  account,
  isAdmin,
  callerID,
  onLogin,
  onRename,
  onToggleVisibility,
  onDelete,
  onRefresh,
}: AccountRowProps) {
  const { t } = useTranslation('claude-accounts')
  const [expanded, setExpanded] = useState(false)

  const isDefault = account.config_dir === ''
  const canManage = isAdmin || account.created_by === callerID

  return (
    <div>
      <div className="flex items-center gap-3 px-4 py-3 border border-border rounded-lg bg-card">
        {/* Chevron toggle — ARIA disclosure pattern */}
        <button
          type="button"
          onClick={() => setExpanded((v) => !v)}
          aria-label={t(expanded ? 'collapse' : 'expand')}
          aria-expanded={expanded}
          aria-controls={`account-usage-${account.id}`}
          className="text-muted-foreground hover:text-foreground p-1 -ml-1 rounded hover:bg-muted/30"
        >
          {expanded ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
        </button>

        {/* Identity column */}
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-1.5 flex-wrap">
            <span className="text-sm font-medium text-foreground truncate">
              {account.name}
            </span>
            {isDefault && (
              <span className="inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-medium bg-info/15 text-info">
                {t('row.default')}
              </span>
            )}
            {account.visibility === 'private' && (
              <span className="inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-medium bg-muted text-muted-foreground">
                {t('visibility.private')}
              </span>
            )}
          </div>
          <p className="text-xs text-muted-foreground mt-0.5">
            {account.email ?? t('row.noEmail')}
          </p>
        </div>

        {/* Status + last-used */}
        <div className="flex flex-col items-end gap-0.5 shrink-0">
          <StatusBadge status={account.status} />
          <span className="text-[10px] text-muted-foreground">
            {account.last_used_at
              ? t('row.lastUsed', { time: new Date(account.last_used_at * 1000).toLocaleDateString() })
              : t('row.neverUsed')}
          </span>
        </div>

        {/* Actions */}
        <div className="flex items-center gap-1 shrink-0">
          {canManage && (
            <Button
              variant="ghost"
              size="sm"
              className="h-7 w-7 p-0"
              title={t('row.actions.login')}
              onClick={() => onLogin(account)}
            >
              <LogIn className="h-3.5 w-3.5" />
            </Button>
          )}
          {canManage && (
            <Button
              variant="ghost"
              size="sm"
              className="h-7 w-7 p-0"
              title={t('row.actions.rename')}
              onClick={() => onRename(account)}
            >
              <Pencil className="h-3.5 w-3.5" />
            </Button>
          )}
          {canManage && !isDefault && (
            <Button
              variant="ghost"
              size="sm"
              className="h-7 w-7 p-0"
              title={t('row.actions.visibility')}
              onClick={() => onToggleVisibility(account)}
            >
              <Eye className="h-3.5 w-3.5" />
            </Button>
          )}
          {canManage && (
            <Button
              variant="ghost"
              size="sm"
              className="h-7 w-7 p-0"
              title={t('row.actions.refresh')}
              onClick={() => onRefresh(account)}
            >
              <RefreshCw className="h-3.5 w-3.5" />
            </Button>
          )}
          {canManage && !isDefault && (
            <Button
              variant="ghost"
              size="sm"
              className="h-7 w-7 p-0 text-destructive hover:text-destructive"
              title={t('row.actions.delete')}
              onClick={() => onDelete(account)}
            >
              <Trash2 className="h-3.5 w-3.5" />
            </Button>
          )}
        </div>
      </div>
      {expanded && (
        <div
          id={`account-usage-${account.id}`}
          role="region"
          aria-label={t('expand')}
          className="mt-2 ml-6 pl-4 border-l-2 border-border"
        >
          <AccountUsageCard accountId={account.id} />
        </div>
      )}
    </div>
  )
}
