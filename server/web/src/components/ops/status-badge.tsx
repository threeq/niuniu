import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import type { OpsBotStatus } from '@/types/ops'

// Status colors are semantic tokens only (design system §2.3): running=success,
// paused=warning, disabled=destructive. The label is always rendered as text, so
// colour is never the only signal.
const STATUS_CLASS: Record<string, string> = {
  active: 'border-success/30 bg-success/15 text-success',
  paused: 'border-warning/40 bg-warning/15 text-warning',
  disabled: 'border-destructive/30 bg-destructive/10 text-destructive-text',
}

export function OpsStatusBadge({
  status,
  className,
}: {
  status: OpsBotStatus
  className?: string
}) {
  const { t } = useTranslation('ops')
  return (
    <Badge
      variant="outline"
      className={cn('rounded px-1.5 py-0.5 text-xs', STATUS_CLASS[status], className)}
    >
      {t(`status.${status}`, { defaultValue: status })}
    </Badge>
  )
}
