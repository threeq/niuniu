import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import type { CodexAccount } from '@/types/api'

interface CodexAccountRowProps {
  account: CodexAccount
  isAdmin: boolean
  callerID: number
  onLogin: (account: CodexAccount) => void
  onRename: (account: CodexAccount) => void
  onToggleVisibility: (account: CodexAccount) => void
  onDelete: (account: CodexAccount) => void
  onRefresh: (account: CodexAccount) => void
}

function formatRelativeTime(seconds: number | undefined): string | null {
  if (!seconds) return null
  const now = Math.floor(Date.now() / 1000)
  const diff = now - seconds
  if (diff < 60) return `${diff}s`
  if (diff < 3600) return `${Math.floor(diff / 60)}m`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h`
  return `${Math.floor(diff / 86400)}d`
}

export function CodexAccountRow({
  account,
  isAdmin,
  callerID,
  onLogin,
  onRename,
  onToggleVisibility,
  onDelete,
  onRefresh,
}: CodexAccountRowProps) {
  const { t } = useTranslation('codex-accounts')
  const canMutate = isAdmin || account.created_by === callerID
  const statusVariant =
    account.status === 'active' ? 'default'
    : account.status === 'failed' ? 'destructive'
    : 'secondary'
  const lastUsed = formatRelativeTime(account.last_used_at)

  return (
    <div className="flex items-center justify-between gap-3 border border-border rounded-md px-4 py-3">
      <div className="flex flex-col min-w-0 gap-1">
        <div className="flex items-center gap-2">
          <span className="font-medium truncate">{account.name}</span>
          <Badge variant={statusVariant}>{t(`status.${account.status}`)}</Badge>
          <Badge variant="outline">{t(`visibility.${account.visibility}`)}</Badge>
        </div>
        <div className="text-xs text-muted-foreground truncate">
          {account.email || t('row.noEmail')}
          {lastUsed
            ? ` · ${t('row.lastUsed', { time: lastUsed })}`
            : ` · ${t('row.neverUsed')}`}
        </div>
      </div>
      <div className="flex items-center gap-1.5">
        {canMutate && account.status !== 'active' && (
          <Button size="sm" variant="default" onClick={() => onLogin(account)}>
            {t('row.actions.login')}
          </Button>
        )}
        {canMutate && (
          <Button size="sm" variant="ghost" onClick={() => onRefresh(account)}>
            {t('row.actions.refresh')}
          </Button>
        )}
        {canMutate && (
          <Button size="sm" variant="ghost" onClick={() => onRename(account)}>
            {t('row.actions.rename')}
          </Button>
        )}
        {canMutate && (
          <Button size="sm" variant="ghost" onClick={() => onToggleVisibility(account)}>
            {t('visibility.switchTo', {
              v: t(`visibility.${account.visibility === 'public' ? 'private' : 'public'}`),
            })}
          </Button>
        )}
        {canMutate && (
          <Button size="sm" variant="ghost" onClick={() => onDelete(account)}>
            {t('row.actions.delete')}
          </Button>
        )}
      </div>
    </div>
  )
}
