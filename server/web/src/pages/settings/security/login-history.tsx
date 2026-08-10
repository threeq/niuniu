import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { CheckCircle2, XCircle, RotateCcw, History } from 'lucide-react'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { Button } from '@/components/ui/button'
import { LoadingSkeleton } from '@/components/shared/loading-skeleton'
import { EmptyState } from '@/components/shared/empty-state'
import { api } from '@/lib/api'
import type { LoginAttempt, LoginAttemptReason } from '@/types/api'

// ------------------------------------------------------------------
// Relative-time helper (without external deps)
// ------------------------------------------------------------------
function formatRelativeTime(isoDate: string): string {
  const diff = Date.now() - new Date(isoDate).getTime()
  const mins = Math.floor(diff / 60_000)
  if (mins < 1) return '< 1m ago'
  if (mins < 60) return `${mins}m ago`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  return `${days}d ago`
}

// ------------------------------------------------------------------
// Reason badge
// ------------------------------------------------------------------
type ReasonBadgeProps = { reason: LoginAttemptReason; success: boolean }

const KNOWN_REASONS = ['ok', 'bad_password', 'account_locked', 'ip_locked', 'mfa_failed'] as const

function ReasonBadge({ reason, success }: ReasonBadgeProps) {
  const { t } = useTranslation('settings')

  const resolvedKey = (KNOWN_REASONS as readonly string[]).includes(reason) ? reason : 'other'
  const label = t(`security.loginHistory.reason.${resolvedKey}`)

  let className: string
  if (success) {
    className =
      'inline-flex items-center rounded px-1.5 py-0.5 text-xs bg-success/15 text-success border border-success/30'
  } else if (reason === 'account_locked' || reason === 'ip_locked') {
    className =
      'inline-flex items-center rounded px-1.5 py-0.5 text-xs bg-destructive/15 text-destructive border border-destructive/30'
  } else {
    className =
      'inline-flex items-center rounded px-1.5 py-0.5 text-xs bg-muted text-muted-foreground border border-warm-border'
  }

  return <span className={className}>{label}</span>
}

// ------------------------------------------------------------------
// Main component
// ------------------------------------------------------------------
export default function LoginHistory() {
  const { t } = useTranslation('settings')

  const {
    data,
    isLoading,
    isError,
    refetch,
  } = useQuery({
    queryKey: ['login-history'],
    queryFn: () => api.getLoginHistory(),
  })

  if (isLoading) {
    return (
      <div className="space-y-2 pt-2">
        <LoadingSkeleton variant="text" count={5} className="h-8 w-full" />
      </div>
    )
  }

  if (isError) {
    return (
      <div className="flex items-center gap-3 py-4 text-sm text-destructive">
        <span>{t('security.loginHistory.empty')}</span>
        <Button variant="ghost" size="sm" onClick={() => void refetch()}>
          <RotateCcw className="w-3.5 h-3.5 mr-1" aria-hidden="true" />
          {t('security.loginHistory.retry')}
        </Button>
      </div>
    )
  }

  const items: LoginAttempt[] = data?.items ?? []

  if (items.length === 0) {
    return (
      <EmptyState
        size="compact"
        icon={<History className="w-10 h-10 text-muted-foreground" aria-hidden="true" />}
        title={t('security.loginHistory.empty')}
      />
    )
  }

  return (
    <TooltipProvider>
      <div className="overflow-x-auto rounded-lg border border-warm-border">
        <table className="w-full text-sm">
          <thead>
            <tr className="bg-warm-muted border-b border-warm-border">
              <th className="text-left px-4 py-2.5 text-xs font-medium text-warm-text-muted">
                {t('security.loginHistory.col.time')}
              </th>
              <th className="text-left px-4 py-2.5 text-xs font-medium text-warm-text-muted">
                {t('security.loginHistory.col.ip')}
              </th>
              <th className="text-left px-4 py-2.5 text-xs font-medium text-warm-text-muted">
                {t('security.loginHistory.col.ua')}
              </th>
              <th className="text-left px-4 py-2.5 text-xs font-medium text-warm-text-muted">
                {t('security.loginHistory.col.result')}
              </th>
            </tr>
          </thead>
          <tbody>
            {items.map((item, idx) => (
              <tr
                key={item.id}
                className={idx % 2 === 0 ? 'bg-warm-surface' : 'bg-warm-muted/50'}
              >
                {/* Time */}
                <td className="px-4 py-3 whitespace-nowrap tabular-nums">
                  <Tooltip>
                    <TooltipTrigger asChild>
                      <span className="text-warm-text-muted cursor-default">
                        {formatRelativeTime(item.created_at)}
                      </span>
                    </TooltipTrigger>
                    <TooltipContent>
                      {new Date(item.created_at).toLocaleString()}
                    </TooltipContent>
                  </Tooltip>
                </td>

                {/* IP */}
                <td className="px-4 py-3 font-mono text-xs text-warm-text whitespace-nowrap">
                  {item.ip}
                </td>

                {/* UA — truncated with full UA in tooltip */}
                <td className="px-4 py-3 max-w-xs">
                  <Tooltip>
                    <TooltipTrigger asChild>
                      <span className="block truncate text-warm-text-muted cursor-default">
                        {item.user_agent}
                      </span>
                    </TooltipTrigger>
                    <TooltipContent className="max-w-sm break-all">
                      {item.user_agent}
                    </TooltipContent>
                  </Tooltip>
                </td>

                {/* Result */}
                <td className="px-4 py-3">
                  <div className="flex items-center gap-2">
                    {item.success ? (
                      <CheckCircle2
                        className="w-4 h-4 text-success shrink-0"
                        aria-hidden="true"
                      />
                    ) : (
                      <XCircle
                        className="w-4 h-4 text-destructive shrink-0"
                        aria-hidden="true"
                      />
                    )}
                    <ReasonBadge reason={item.reason} success={item.success} />
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </TooltipProvider>
  )
}
