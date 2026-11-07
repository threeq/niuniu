import { useTranslation } from 'react-i18next'
import { AlertTriangle, OctagonAlert } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import type { OpsRiskLevel } from '@/types/ops'

const RISK_CLASS: Record<string, string> = {
  normal: 'border-success/30 bg-success/15 text-success',
  warning: 'border-warning/40 bg-warning/15 text-warning',
  critical: 'border-destructive/30 bg-destructive/10 text-destructive-text',
}

/** Risk is stated in words as well as colour; the reasons stay available as a
 *  title tooltip so a narrow row does not become a wall of text. */
export function OpsRiskBadge({
  level,
  reasons = [],
  className,
}: {
  level: OpsRiskLevel
  reasons?: string[]
  className?: string
}) {
  const { t } = useTranslation('ops')
  const reasonText = reasons
    .map((reason) => t(`riskReason.${reason}`, { defaultValue: reason }))
    .join(' · ')
  const Icon = level === 'critical' ? OctagonAlert : level === 'warning' ? AlertTriangle : null
  return (
    <Badge
      variant="outline"
      className={cn('gap-1 rounded px-1.5 py-0.5 text-xs', RISK_CLASS[level], className)}
      title={reasonText || undefined}
    >
      {Icon ? <Icon className="h-3 w-3" aria-hidden="true" /> : null}
      {t(`risk.${level}`, { defaultValue: level })}
    </Badge>
  )
}
