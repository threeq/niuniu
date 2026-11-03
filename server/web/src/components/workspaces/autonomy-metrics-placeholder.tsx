import type { ComponentType } from 'react'
import { useTranslation } from 'react-i18next'
import { Inbox, Zap } from 'lucide-react'
import { Badge } from '@/components/ui/badge'

// Mission control placeholders for the Intake / Completion panels. Phase 0
// ships the layout only — values stay "—" until the metrics endpoints land.

interface MetricPanelProps {
  title: string
  subtitle: string
  icon: ComponentType<{ className?: string }>
  metrics: string[]
  comingSoon: string
  noData: string
}

function MetricPanel({ title, subtitle, icon: Icon, metrics, comingSoon, noData }: MetricPanelProps) {
  return (
    <div className="rounded-lg border bg-card p-4 space-y-3">
      <div className="flex items-center gap-2">
        <Icon className="w-4 h-4 text-muted-foreground" aria-hidden="true" />
        <span className="text-sm font-medium">{title}</span>
        <Badge variant="outline" className="ml-auto text-xs font-normal text-muted-foreground">
          {comingSoon}
        </Badge>
      </div>
      <p className="text-xs text-muted-foreground">{subtitle}</p>
      <div className="grid grid-cols-2 gap-3">
        {metrics.map((label) => (
          <div key={label}>
            <div className="text-xs text-muted-foreground">{label}</div>
            <div className="text-lg font-semibold tabular-nums text-muted-foreground">{noData}</div>
          </div>
        ))}
      </div>
    </div>
  )
}

export function AutonomyMetricsPlaceholder() {
  const { t } = useTranslation('intake')
  const noData = t('missionControl.noData')
  const comingSoon = t('missionControl.comingSoon')

  return (
    <div className="grid gap-4 md:grid-cols-2">
      <MetricPanel
        title={t('missionControl.intakeTitle')}
        subtitle={t('missionControl.intakeSubtitle')}
        icon={Inbox}
        metrics={[
          t('missionControl.todayAccepted'),
          t('missionControl.pending'),
          t('missionControl.misfireRate'),
          t('missionControl.duplicateRate'),
        ]}
        comingSoon={comingSoon}
        noData={noData}
      />
      <MetricPanel
        title={t('missionControl.completionTitle')}
        subtitle={t('missionControl.completionSubtitle')}
        icon={Zap}
        metrics={[
          t('missionControl.inProgress'),
          t('missionControl.firstPassRate'),
          t('missionControl.avgRounds'),
          t('missionControl.interventions'),
          t('missionControl.todayTokens'),
        ]}
        comingSoon={comingSoon}
        noData={noData}
      />
    </div>
  )
}
